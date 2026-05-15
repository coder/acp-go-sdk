package httpserver

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// H2CHandler wraps h with an h2c (cleartext HTTP/2) upgrader. The RFD
// requires HTTP/2 for the Streamable HTTP transport; on cleartext
// listeners net/http only negotiates HTTP/2 via TLS+ALPN, so callers
// who want to expose the transport over plain HTTP must wrap their
// handler in h2c.
//
// h2c.NewHandler is fully backwards-compatible with HTTP/1.1: requests
// without the h2c upgrade preface are served as HTTP/1.1, so this
// wrapper is always safe to apply.
func H2CHandler(h http.Handler) http.Handler {
	return h2c.NewHandler(h, &http2.Server{})
}

// BuildHTTPServer returns an *http.Server that wraps the receiver's
// Handler() with h2c and uses sensible defaults for SSE workloads:
//
//   - ReadTimeout/WriteTimeout are 0 (SSE streams live for the entire
//     session and must not be cut off by a server-side timeout).
//   - IdleTimeout is 120s so idle keep-alive connections eventually
//     recycle.
//
// Callers who need to install additional middleware (access logging,
// authentication, metrics) should call Handler() directly and wrap that
// themselves; this helper is a convenience for the common path.
//
// The returned *http.Server has no Addr set; pass the listener to
// Serve(), or set Addr and call ListenAndServe.
func (s *Server) BuildHTTPServer() *http.Server {
	return &http.Server{
		Handler:      H2CHandler(s.Handler()),
		ReadTimeout:  0,
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}
}

// ListenAndServe binds the server to addr (host:port) and serves until
// ctx is cancelled or the listener fails. It performs a graceful
// shutdown (up to shutdownTimeout, default 5s) when ctx is cancelled,
// then tears down all active ACP connections.
//
// The handler is h2c-wrapped automatically. For full control over the
// listener, TLS configuration, or middleware composition, use
// BuildHTTPServer (or Handler() directly) and run the *http.Server
// yourself.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return s.Serve(ctx, ln)
}

// Serve serves on ln until ctx is cancelled or the listener fails. It is
// the lower-level half of ListenAndServe and lets callers bring their
// own listener (e.g. a Unix domain socket, or one wrapped in TLS).
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	httpSrv := s.BuildHTTPServer()

	serveErr := make(chan error, 1)
	go func() {
		err := httpSrv.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveErr <- err
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
		_ = s.Close()
		// Drain the serve goroutine; it should now return cleanly.
		<-serveErr
		return nil
	case err := <-serveErr:
		_ = s.Close()
		return err
	}
}
