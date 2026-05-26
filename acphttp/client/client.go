// Package client implements the ACP HTTP "Streamable HTTP" transport for
// client-side use.
//
// See https://github.com/agentclientprotocol/agent-client-protocol/blob/main/docs/rfds/streamable-http-websocket-transport.mdx
// for the wire-level specification.
//
// The transport bridges the acp-go-sdk's line-delimited JSON io.Reader /
// io.Writer interface to the remote server's /acp endpoint:
//
//   - Client → server JSON-RPC messages are sent via POST /acp. The
//     `initialize` request returns 200 OK with a JSON body carrying the
//     Acp-Connection-Id. All other POSTs return 202 Accepted; their
//     responses flow back on a long-lived GET SSE stream.
//   - Server → client messages are delivered on long-lived GET SSE streams:
//     one connection-scoped stream (carrying responses to session/new,
//     session/load, and connection-level messages) plus one stream per
//     active session (carrying session update notifications,
//     server-initiated requests like request_permission, and responses to
//     session/prompt / session/cancel).
//
// All streams are demultiplexed into a single inbound line-delimited JSON
// stream consumed by the SDK's Connection as if the transport were stdio.
package httpclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sync"
	"time"

	"github.com/coder/acp-go-sdk/acphttp"
	"golang.org/x/net/http2"
	"golang.org/x/net/publicsuffix"
)

const (
	headerConnectionID = acphttp.HeaderConnectionID
	headerSessionID    = acphttp.HeaderSessionID

	mimeJSON = acphttp.MimeJSON
	mimeSSE  = acphttp.MimeSSE

	inboundChanCapacity = 1024
)

// Config configures a Streamable HTTP transport.
type Config struct {
	// BaseURL is the `/acp` endpoint of the remote server (e.g.
	// `http://localhost:3000/acp`). Bare base URLs without a path will have
	// `/acp` appended automatically.
	BaseURL string

	// Headers are additional HTTP headers to send on every request (useful
	// for auth tokens). These are merged on top of the transport's own
	// required headers.
	Headers map[string]string

	// HTTPTimeout is the per-request timeout for non-streaming POSTs.
	// Defaults to 60s. Streaming GETs are never subject to this timeout.
	HTTPTimeout time.Duration

	// Logger is used for internal diagnostics. Defaults to slog.Default().
	Logger *slog.Logger
}

// Transport is a bidirectional line-delimited JSON stream bridging to a
// remote ACP server over Streamable HTTP. It implements io.Reader and
// io.WriteCloser, and is safe for concurrent use by the SDK.
type Transport struct {
	cfg    Config
	url    string
	logger *slog.Logger

	// httpClient handles all non-streaming POSTs (including initialize). Its
	// own Timeout is left at 0; the per-request deadline is enforced via
	// httpTimeout on the request context so failures report a single,
	// unambiguous "context deadline exceeded".
	httpClient *http.Client
	// httpTimeout is the per-request deadline applied to non-streaming POSTs.
	httpTimeout time.Duration
	// streamClient is used for long-lived GET SSE streams (no timeout).
	streamClient *http.Client

	ctx    context.Context
	cancel context.CancelFunc

	connIDMu sync.RWMutex
	connID   string // empty until initialize completes

	sessionsMu  sync.Mutex
	sessionGets map[string]context.CancelFunc // sessionId → stream cancel
	connGetOpen bool
	streams     sync.WaitGroup

	// inbound carries demultiplexed JSON messages (without trailing newlines)
	// from all server-to-client streams plus the initialize response body.
	inbound chan []byte
	// readBuf holds any residual bytes from the current inbound message
	// that didn't fit in a Read() call.
	readBuf bytes.Buffer
	readMu  sync.Mutex

	writeMu sync.Mutex

	closeOnce sync.Once
	closedCh  chan struct{}
}

// Dial creates a Transport. It does not perform any network I/O; the
// initialize POST is triggered by the first JSON-RPC message the SDK writes
// to the Transport.
func Dial(ctx context.Context, cfg Config) (*Transport, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("httpclient: BaseURL is required")
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("httpclient: parse BaseURL %q: %w", cfg.BaseURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("httpclient: unsupported scheme %q (expected http or https)", u.Scheme)
	}
	// If the user gave us a bare origin (e.g. http://localhost:3000), append /acp.
	if u.Path == "" || u.Path == "/" {
		u.Path = "/acp"
	}

	timeout := cfg.HTTPTimeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("component", "acphttp.client", "url", u.String())

	// Cookie jar is required by the RFD: clients MUST accept, store, and
	// return cookies for the duration of the connection. We scope the jar
	// to this transport (a fresh jar per Dial call) so cookies do not
	// leak across unrelated connections.
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return nil, fmt.Errorf("httpclient: cookie jar: %w", err)
	}

	rt, err := buildRoundTripper(u.Scheme)
	if err != nil {
		return nil, err
	}

	httpClient := &http.Client{
		Transport: rt,
		Jar:       jar,
		// Timeout intentionally 0: the per-request deadline is applied via
		// context (httpTimeout) so we don't get two independent timers racing
		// to produce different errors at the same instant.
	}
	streamClient := &http.Client{
		Transport: rt,
		Jar:       jar,
		// No timeout: SSE streams are long-lived.
	}

	tctx, cancel := context.WithCancel(ctx)
	t := &Transport{
		cfg:          cfg,
		url:          u.String(),
		logger:       logger,
		httpClient:   httpClient,
		httpTimeout:  timeout,
		streamClient: streamClient,
		ctx:          tctx,
		cancel:       cancel,
		sessionGets:  make(map[string]context.CancelFunc),
		inbound:      make(chan []byte, inboundChanCapacity),
		closedCh:     make(chan struct{}),
	}
	return t, nil
}

// buildRoundTripper returns an http.RoundTripper appropriate for the URL
// scheme.
//
// For https:// URLs Go's default transport negotiates HTTP/2 via ALPN when
// the server supports it (per the RFD's "HTTP/2 REQUIRED" for Streamable
// HTTP), and falls back to HTTP/1.1 otherwise.
//
// For cleartext http:// URLs we deliberately speak HTTP/1.1: net/http does
// not negotiate HTTP/2 over plain TCP, and real-world reference servers
// (notably `goose serve`) only accept HTTP/1.1 on cleartext — attempting
// prior-knowledge h2c against them hangs during the preface exchange.
// HTTP/1.1 with keep-alive is sufficient for our needs because POSTs and
// long-lived GET streams run on separate connections from the pool.
func buildRoundTripper(scheme string) (http.RoundTripper, error) {
	if scheme == "https" {
		base := &http.Transport{
			ForceAttemptHTTP2:   true,
			MaxConnsPerHost:     0,
			MaxIdleConnsPerHost: 8,
			IdleConnTimeout:     90 * time.Second,
		}
		if err := http2.ConfigureTransport(base); err != nil {
			return nil, fmt.Errorf("httpclient: configure http/2: %w", err)
		}
		return base, nil
	}
	return &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxConnsPerHost:     0,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     90 * time.Second,
		// Explicitly stick to HTTP/1.1 on cleartext: see function comment.
		ForceAttemptHTTP2: false,
		TLSNextProto:      map[string]func(string, *tls.Conn) http.RoundTripper{},
	}, nil
}

// applyExtraHeaders merges the user-configured Headers onto req, without
// overwriting ACP-required headers that the caller has already set.
func (t *Transport) applyExtraHeaders(req *http.Request) {
	for k, v := range t.cfg.Headers {
		if req.Header.Get(k) == "" {
			req.Header.Set(k, v)
		}
	}
}

// setConnID stores the Acp-Connection-Id for use on subsequent requests.
func (t *Transport) setConnID(id string) {
	t.connIDMu.Lock()
	t.connID = id
	t.connIDMu.Unlock()
}

// getConnID returns the current Acp-Connection-Id or "" if initialize has
// not completed.
func (t *Transport) getConnID() string {
	t.connIDMu.RLock()
	defer t.connIDMu.RUnlock()
	return t.connID
}

// isClosed reports whether the transport has been closed via Close() or its
// parent context has been cancelled.
func (t *Transport) isClosed() bool {
	select {
	case <-t.closedCh:
		return true
	case <-t.ctx.Done():
		return true
	default:
		return false
	}
}

// pushInbound enqueues a raw JSON message (without trailing newline) to be
// read by the SDK. It is a no-op if the transport is closed.
func (t *Transport) pushInbound(raw []byte) {
	// Copy the slice: SSE scanners reuse their backing buffer between events.
	msg := make([]byte, len(raw))
	copy(msg, raw)
	select {
	case t.inbound <- msg:
	case <-t.closedCh:
	case <-t.ctx.Done():
	}
}

// peekID returns the JSON-RPC id of raw as a string, suitable for log
// fields. Returns "" for absent ids.
func peekID(raw []byte) string { return string(acphttp.PeekID(raw)) }
