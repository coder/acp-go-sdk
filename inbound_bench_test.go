package acp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The benchmarks in this file read recorded ACP traffic the way a Connection
// reads what its peer sends. scripts/bench (make bench) runs them for two
// commits and compares the results; see testdata/bench/README.md.

// inboundCorpusGlobs name the recorded traffic: the fixtures of the Claude and
// Codex adapters and the verify-acp-go-sdk journeys in testdata/bench, and the
// transcripts that verify-schema-bump's G2 gate round-trips. testdata/bench
// and .agents are modules of their own, so the module zip that the Go proxy
// serves has neither: a test that needs the corpus skips there. In a checkout
// each folder that exists must match its glob; the corpus digest tells the
// corpora apart.
var inboundCorpusGlobs = []struct {
	dir  string
	glob string
}{
	{"testdata/bench", "testdata/bench/*/*.jsonl"},
	{".agents/skills/verify-schema-bump/roundtrip/testdata/transcripts", ".agents/skills/verify-schema-bump/roundtrip/testdata/transcripts/*.jsonl"},
}

// An inboundMessage is one message of the corpus, as the side that receives
// it reads it.
type inboundMessage struct {
	where   string // file:line
	toAgent bool   // the agent receives it; otherwise the client does
	method  string // its method, or for a response the method of its request
	update  string // the sessionUpdate of a session/update notification
	line    []byte // the message as it crosses the wire
}

// loadInboundCorpus reads every message of the corpus. A line of a transcript
// is {"dir","msg"} or, for input that is not JSON, {"dir","raw"}. A response
// whose request the file does not have names the request's method in
// "method".
func loadInboundCorpus(tb testing.TB) []inboundMessage {
	tb.Helper()
	var paths []string
	for _, g := range inboundCorpusGlobs {
		if _, err := os.Stat(g.dir); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		matches, err := filepath.Glob(g.glob)
		if err != nil || len(matches) == 0 {
			tb.Fatalf("no corpus files match %s: %v", g.glob, err)
		}
		paths = append(paths, matches...)
	}
	if len(paths) == 0 {
		tb.Skip("no corpus: this is the module zip, which leaves out testdata/bench and .agents")
	}
	sort.Strings(paths)
	var corpus []inboundMessage
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			tb.Fatal(err)
		}
		// pending maps a request's direction and id to its method.
		pending := map[string]string{}
		sc := bufio.NewScanner(f)
		sc.Buffer(nil, 16<<20)
		for n := 1; sc.Scan(); n++ {
			var rec struct {
				Dir    string          `json:"dir"`
				Method string          `json:"method"`
				Msg    json.RawMessage `json:"msg"`
				Raw    *string         `json:"raw"`
			}
			if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
				tb.Fatalf("%s:%d: %v", path, n, err)
			}
			m := inboundMessage{where: fmt.Sprintf("%s:%d", path, n), toAgent: rec.Dir == "client->agent"}
			if rec.Dir != "client->agent" && rec.Dir != "agent->client" {
				tb.Fatalf("%s: unknown dir %q", m.where, rec.Dir)
			}
			switch {
			case rec.Raw != nil && *rec.Raw == "<EOF>":
				// acpwire marks where it closed the agent's input.
				continue
			case rec.Raw != nil:
				m.line = []byte(*rec.Raw)
			default:
				var buf bytes.Buffer
				if err := json.Compact(&buf, rec.Msg); err != nil {
					tb.Fatalf("%s: %v", m.where, err)
				}
				m.line = buf.Bytes()
				var env struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
					Params struct {
						Update struct {
							SessionUpdate string `json:"sessionUpdate"`
						} `json:"update"`
					} `json:"params"`
				}
				if err := json.Unmarshal(m.line, &env); err != nil {
					tb.Fatalf("%s: %v", m.where, err)
				}
				switch {
				case env.Method != "":
					m.method = env.Method
					if env.ID != nil {
						reply := "client->agent"
						if m.toAgent {
							reply = "agent->client"
						}
						pending[reply+" "+string(env.ID)] = env.Method
					}
					if env.Method == ClientMethodSessionUpdate {
						m.update = env.Params.Update.SessionUpdate
					}
				case rec.Method != "":
					m.method = rec.Method
				default:
					m.method = pending[rec.Dir+" "+string(env.ID)]
				}
			}
			corpus = append(corpus, m)
		}
		f.Close()
		if err := sc.Err(); err != nil {
			tb.Fatal(err)
		}
	}
	return corpus
}

// printCorpus prints the digest of the corpus that a benchmark reads, once,
// as a benchstat configuration line, so that benchstat keeps results of two
// different corpora apart.
func printCorpus(b *testing.B, msgs []inboundMessage) {
	key := b.Name()
	if i := strings.IndexByte(key, '/'); i >= 0 {
		key = key[:i]
	}
	if _, done := printedCorpus.LoadOrStore(key, true); !done {
		fmt.Printf("corpus-%s: %s %d msgs\n", strings.TrimPrefix(key, "Benchmark"), corpusDigest(msgs), len(msgs))
	}
}

var printedCorpus sync.Map

// corpusDigest returns a short hash of the messages, so that a benchmark
// result names the corpus that it read.
func corpusDigest(corpus []inboundMessage) string {
	h := sha256.New()
	for _, m := range corpus {
		h.Write(m.line)
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// benchPeers holds an agent-side and a client-side Connection whose
// dispatchers decode the corpus. Their peers never write, so their receive
// loops wait until Close.
type benchPeers struct {
	agent       *AgentSideConnection
	client      *ClientSideConnection
	agentImpl   *benchAgent
	clientImpl  *benchClient
	closeInputs func()
}

func newBenchPeers() *benchPeers {
	agentIn, agentInW := io.Pipe()
	clientIn, clientInW := io.Pipe()
	p := &benchPeers{agentImpl: &benchAgent{}, clientImpl: &benchClient{}}
	p.agent = NewAgentSideConnection(p.agentImpl, io.Discard, agentIn)
	p.client = NewClientSideConnection(p.clientImpl, io.Discard, clientIn)
	p.agent.SetLogger(quietLogger)
	p.client.SetLogger(quietLogger)
	p.closeInputs = func() {
		agentInW.Close()
		clientInW.Close()
		<-p.agent.Done()
		<-p.client.Done()
	}
	return p
}

// decode reads m as the Connection that receives it does: it decodes the
// JSON-RPC envelope, then passes a request or a notification to the
// dispatcher, which decodes and validates its params, or decodes the result
// of a response into the type that SendRequest returns. It returns the error
// that the connection would see. A non-nil r keeps the decoded result.
func (p *benchPeers) decode(ctx context.Context, m *inboundMessage, r *recorder) error {
	var msg anyMessage
	if err := json.Unmarshal(m.line, &msg); err != nil {
		return err
	}
	switch {
	case msg.ID == nil && msg.Method == "$/cancel_request":
		// The receive loop handles it itself; no request is in flight.
		p.agent.conn.handleCancelRequest(&msg)
		return nil
	case msg.Method != "":
		handle := p.client.handleWithExtensions
		if m.toAgent {
			handle = p.agent.handleWithExtensions
		}
		if _, err := handle(ctx, msg.Method, msg.Params); err != nil {
			return err
		}
		return nil
	case msg.Error != nil || len(msg.Result) == 0:
		return nil
	}
	results := clientResults
	if m.toAgent {
		results = agentResults
	}
	res, ok := results[m.method]
	if !ok {
		return fmt.Errorf("no result type for %q", m.method)
	}
	return res.decode(msg.Result, r)
}

// A resultType decodes the result of a response as SendRequest[T] does.
type resultType struct {
	name   string
	decode func(raw json.RawMessage, r *recorder) error
}

func resultOf[T any]() resultType {
	return resultType{
		name: reflect.TypeOf((*T)(nil)).Elem().Name(),
		decode: func(raw json.RawMessage, r *recorder) error {
			var v T
			if err := json.Unmarshal(raw, &v); err != nil {
				return err
			}
			if r != nil {
				record(r, v)
			}
			return nil
		},
	}
}

// agentResults and clientResults name the result type of each request that
// the agent and the client send, by its method. They mirror the SendRequest
// calls in agent_gen.go and client_gen.go, which
// TestInboundBench_ResultTypes checks.
var agentResults = map[string]resultType{
	ClientMethodElicitationCreate:        resultOf[CreateElicitationResponse](),
	ClientMethodFsReadTextFile:           resultOf[ReadTextFileResponse](),
	ClientMethodFsWriteTextFile:          resultOf[WriteTextFileResponse](),
	ClientMethodMcpMessage:               resultOf[UnstableMessageMcpResponse](),
	ClientMethodSessionRequestPermission: resultOf[RequestPermissionResponse](),
	ClientMethodTerminalCreate:           resultOf[CreateTerminalResponse](),
	ClientMethodTerminalKill:             resultOf[KillTerminalResponse](),
	ClientMethodTerminalOutput:           resultOf[TerminalOutputResponse](),
	ClientMethodTerminalRelease:          resultOf[ReleaseTerminalResponse](),
	ClientMethodTerminalWaitForExit:      resultOf[WaitForTerminalExitResponse](),
}

var clientResults = map[string]resultType{
	AgentMethodAuthStatus:             resultOf[UnstableAuthStatusResponse](),
	AgentMethodAuthenticate:           resultOf[AuthenticateResponse](),
	AgentMethodInitialize:             resultOf[InitializeResponse](),
	AgentMethodLogout:                 resultOf[LogoutResponse](),
	AgentMethodProvidersDisable:       resultOf[UnstableDisableProviderResponse](),
	AgentMethodProvidersList:          resultOf[UnstableListProvidersResponse](),
	AgentMethodProvidersSet:           resultOf[UnstableSetProviderResponse](),
	AgentMethodSessionArchive:         resultOf[UnstableArchiveSessionResponse](),
	AgentMethodSessionClose:           resultOf[CloseSessionResponse](),
	AgentMethodSessionDelete:          resultOf[DeleteSessionResponse](),
	AgentMethodSessionFork:            resultOf[UnstableForkSessionResponse](),
	AgentMethodSessionList:            resultOf[ListSessionsResponse](),
	AgentMethodSessionLoad:            resultOf[LoadSessionResponse](),
	AgentMethodSessionNew:             resultOf[NewSessionResponse](),
	AgentMethodSessionOptions:         resultOf[UnstableSessionOptionsResponse](),
	AgentMethodSessionPrompt:          resultOf[PromptResponse](),
	AgentMethodSessionResume:          resultOf[ResumeSessionResponse](),
	AgentMethodSessionSetConfigOption: resultOf[SetSessionConfigOptionResponse](),
	AgentMethodSessionSetMode:         resultOf[SetSessionModeResponse](),
	AgentMethodSessionSetTitle:        resultOf[UnstableSetSessionTitleResponse](),
	AgentMethodSessionStatus:          resultOf[UnstableSessionStatusResponse](),
	AgentMethodSessionUnarchive:       resultOf[UnstableUnarchiveSessionResponse](),
}

// BenchmarkInbound reads every message of the corpus once per op, as the
// Connection that receives it does. ns/op is the time to read the corpus.
func BenchmarkInbound(b *testing.B) {
	corpus := loadInboundCorpus(b)
	b.Run("corpus", func(b *testing.B) { benchmarkDecode(b, corpus) })
}

// BenchmarkInboundStream reads the bursts of streamed session updates in the
// corpus: the agent_message_chunk and tool_call_update notifications.
func BenchmarkInboundStream(b *testing.B) {
	corpus := loadInboundCorpus(b)
	for _, kind := range []string{"agent_message_chunk", "tool_call_update"} {
		var burst []inboundMessage
		for _, m := range corpus {
			if m.update == kind {
				burst = append(burst, m)
			}
		}
		b.Run(kind, func(b *testing.B) { benchmarkDecode(b, burst) })
	}
}

func benchmarkDecode(b *testing.B, msgs []inboundMessage) {
	if len(msgs) == 0 {
		b.Fatal("no messages")
	}
	printCorpus(b, msgs)
	p := newBenchPeers()
	defer p.closeInputs()
	ctx := context.Background()
	var size int64
	for _, m := range msgs {
		size += int64(len(m.line))
	}
	b.SetBytes(size)
	b.ReportAllocs()
	b.ResetTimer()
	cpu := processCPU()
	var errs int
	for i := 0; i < b.N; i++ {
		errs = 0
		for j := range msgs {
			if p.decode(ctx, &msgs[j], nil) != nil {
				errs++
			}
		}
	}
	b.StopTimer()
	reportCPU(b, cpu)
	b.ReportMetric(float64(len(msgs)), "msgs/op")
	b.ReportMetric(float64(errs), "errors/op")
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(len(msgs)), "ns/msg")
}

// BenchmarkConnectionPipe streams the corpus's session/update notifications
// through a Connection over an io.Pipe, with at most streamWindow of them
// sent and not yet handled. In "pair" an AgentSideConnection sends them to a
// ClientSideConnection, so the agent encodes each one; in "inbound" the
// benchmark writes the recorded lines to the ClientSideConnection itself.
func BenchmarkConnectionPipe(b *testing.B) {
	var lines [][]byte
	var notes []SessionNotification
	for _, m := range loadInboundCorpus(b) {
		if m.method != ClientMethodSessionUpdate || m.toAgent {
			continue
		}
		var msg anyMessage
		var n SessionNotification
		if json.Unmarshal(m.line, &msg) != nil || json.Unmarshal(msg.Params, &n) != nil || n.Validate() != nil {
			continue
		}
		lines = append(lines, append(append([]byte{}, m.line...), '\n'))
		notes = append(notes, n)
	}
	if len(notes) == 0 {
		b.Fatal("no session/update notifications")
	}
	b.Run("pair", func(b *testing.B) {
		benchmarkPipe(b, len(notes), func(clientIn io.Writer) (func(int), func()) {
			agentIn, closeAgentIn := io.Pipe()
			agent := NewAgentSideConnection(&benchAgent{}, clientIn, agentIn)
			agent.SetLogger(quietLogger)
			ctx := context.Background()
			send := func(i int) {
				if err := agent.SessionUpdate(ctx, notes[i]); err != nil {
					b.Fatal(err)
				}
			}
			return send, func() {
				closeAgentIn.Close()
				<-agent.Done()
			}
		})
	})
	b.Run("inbound", func(b *testing.B) {
		benchmarkPipe(b, len(lines), func(clientIn io.Writer) (func(int), func()) {
			send := func(i int) {
				if _, err := clientIn.Write(lines[i]); err != nil {
					b.Fatal(err)
				}
			}
			return send, func() {}
		})
	})
}

// reportCPU reports the CPU time per op that the process used since start,
// in every goroutine and the garbage collector, where processCPU can tell.
func reportCPU(b *testing.B, start time.Duration) {
	if used := processCPU() - start; start > 0 {
		b.ReportMetric(float64(used.Nanoseconds())/float64(b.N), "cpu-ns/op")
	}
}

// quietLogger drops the connections' logs, such as "connection closed".
var quietLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

// streamWindow is how many notifications the stream benchmarks keep in
// flight. It stays below defaultMaxQueuedNotifications, because a Connection
// closes when its peer floods the notification queue.
const streamWindow = 256

// benchmarkPipe starts a ClientSideConnection that reads from a pipe, and
// sends n notifications to it per op with the send func that start returns.
// stop ends what start began.
func benchmarkPipe(b *testing.B, n int, start func(clientIn io.Writer) (send func(int), stop func())) {
	clientIn, clientInW := io.Pipe()
	client := &streamClient{window: make(chan struct{}, streamWindow)}
	conn := NewClientSideConnection(client, io.Discard, clientIn)
	conn.SetLogger(quietLogger)
	send, stop := start(clientInW)
	b.ReportAllocs()
	b.ResetTimer()
	cpu := processCPU()
	for i := 0; i < b.N; i++ {
		for j := 0; j < n; j++ {
			client.window <- struct{}{}
			send(j)
		}
		// Wait until the client has handled every notification.
		for k := 0; k < streamWindow; k++ {
			client.window <- struct{}{}
		}
		for k := 0; k < streamWindow; k++ {
			<-client.window
		}
	}
	b.StopTimer()
	reportCPU(b, cpu)
	stop()
	clientInW.Close()
	<-conn.Done()
	if got, want := client.handled.Load(), int64(n)*int64(b.N); got != want {
		b.Fatalf("handled %d notifications, want %d", got, want)
	}
	b.ReportMetric(float64(n), "msgs/op")
	b.ReportMetric(float64(n)*float64(b.N)/b.Elapsed().Seconds(), "msgs/s")
}

// streamClient handles session/update notifications and frees a slot of the
// window for each.
type streamClient struct {
	benchClient
	window  chan struct{}
	handled atomic.Int64
}

func (c *streamClient) SessionUpdate(context.Context, SessionNotification) error {
	c.handled.Add(1)
	<-c.window
	return nil
}

// TestInboundCorpus reads the corpus as BenchmarkInbound does. With
// ACP_BENCH_OUTCOMES set to a file, it writes the outcome of each message
// there: a hash of the value that it decoded to, encoded again, or the error.
// scripts/bench compares the outcomes of two commits, so a faster decoder
// must also read the corpus the same way.
func TestInboundCorpus(t *testing.T) {
	corpus := loadInboundCorpus(t)
	p := newBenchPeers()
	defer p.closeInputs()
	ctx := context.Background()
	var out bytes.Buffer
	var errs int
	for i := range corpus {
		m := &corpus[i]
		// Each side and the result decoder record into r.
		r := recorder{on: true}
		p.agentImpl.recorder, p.clientImpl.recorder = recorder{on: true}, recorder{on: true}
		err := p.decode(ctx, m, &r)
		for _, side := range []*recorder{&p.agentImpl.recorder, &p.clientImpl.recorder} {
			if side.got != nil {
				r.got = side.got
			}
		}
		fmt.Fprintf(&out, "%s %s ", m.where, m.method)
		switch {
		case err != nil:
			errs++
			fmt.Fprintf(&out, "error %v\n", err)
		case r.got == nil:
			out.WriteString("ok\n")
		default:
			enc, err := json.Marshal(r.got)
			if err != nil {
				fmt.Fprintf(&out, "encode error %v\n", err)
				continue
			}
			sum := sha256.Sum256(enc)
			fmt.Fprintf(&out, "ok %x\n", sum[:8])
		}
	}
	t.Logf("corpus %s: %d messages, %d errors", corpusDigest(corpus), len(corpus), errs)
	if path := os.Getenv("ACP_BENCH_OUTCOMES"); path != "" {
		if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestInboundBench_ResultTypes checks that agentResults and clientResults
// name the result type of each request as the SendRequest calls in
// agent_gen.go and client_gen.go do.
func TestInboundBench_ResultTypes(t *testing.T) {
	call := regexp.MustCompile(`SendRequest\[(\w+)\]\(c\.conn, ctx, (\w+),`)
	methods := methodConstants(t)
	for file, results := range map[string]map[string]resultType{
		"agent_gen.go":  agentResults,
		"client_gen.go": clientResults,
	} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		calls := call.FindAllStringSubmatch(string(src), -1)
		if len(calls) != len(results) {
			t.Errorf("%s has %d SendRequest calls, the table %d entries", file, len(calls), len(results))
		}
		for _, c := range calls {
			if got := results[methods[c[2]]].name; got != c[1] {
				t.Errorf("%s: the result of %s is %s, the table has %q", file, c[2], c[1], got)
			}
		}
	}
}

// methodConstants maps the names of the method constants in
// constants_gen.go to their values.
func methodConstants(t *testing.T) map[string]string {
	src, err := os.ReadFile("constants_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	methods := map[string]string{}
	for _, m := range regexp.MustCompile(`(?m)^\s+((?:Agent|Client)Method\w+)\s+= "([^"]+)"`).FindAllStringSubmatch(string(src), -1) {
		methods[m[1]] = m[2]
	}
	return methods
}

// A recorder keeps the last value that a dispatcher decoded, when on.
type recorder struct {
	on  bool
	got any
}

func record[T any](r *recorder, v T) {
	if r.on {
		r.got = v
	}
}

// benchAgent and benchClient implement every method of their side, and
// keep the params that they get when they record.
type benchAgent struct{ recorder }

type benchClient struct{ recorder }

func (s *benchAgent) Authenticate(_ context.Context, p AuthenticateRequest) (AuthenticateResponse, error) {
	record(&s.recorder, p)
	return AuthenticateResponse{}, nil
}

func (s *benchAgent) Cancel(_ context.Context, p CancelNotification) error {
	record(&s.recorder, p)
	return nil
}

func (s *benchAgent) CloseSession(_ context.Context, p CloseSessionRequest) (CloseSessionResponse, error) {
	record(&s.recorder, p)
	return CloseSessionResponse{}, nil
}

func (s *benchAgent) DeleteSession(_ context.Context, p DeleteSessionRequest) (DeleteSessionResponse, error) {
	record(&s.recorder, p)
	return DeleteSessionResponse{}, nil
}

func (s *benchAgent) Initialize(_ context.Context, p InitializeRequest) (InitializeResponse, error) {
	record(&s.recorder, p)
	return InitializeResponse{}, nil
}

func (s *benchAgent) ListSessions(_ context.Context, p ListSessionsRequest) (ListSessionsResponse, error) {
	record(&s.recorder, p)
	return ListSessionsResponse{}, nil
}

func (s *benchAgent) LoadSession(_ context.Context, p LoadSessionRequest) (LoadSessionResponse, error) {
	record(&s.recorder, p)
	return LoadSessionResponse{}, nil
}

func (s *benchAgent) Logout(_ context.Context, p LogoutRequest) (LogoutResponse, error) {
	record(&s.recorder, p)
	return LogoutResponse{}, nil
}

func (s *benchAgent) NewSession(_ context.Context, p NewSessionRequest) (NewSessionResponse, error) {
	record(&s.recorder, p)
	return NewSessionResponse{}, nil
}

func (s *benchAgent) Prompt(_ context.Context, p PromptRequest) (PromptResponse, error) {
	record(&s.recorder, p)
	return PromptResponse{}, nil
}

func (s *benchAgent) ResumeSession(_ context.Context, p ResumeSessionRequest) (ResumeSessionResponse, error) {
	record(&s.recorder, p)
	return ResumeSessionResponse{}, nil
}

func (s *benchAgent) SetSessionConfigOption(_ context.Context, p SetSessionConfigOptionRequest) (SetSessionConfigOptionResponse, error) {
	record(&s.recorder, p)
	return SetSessionConfigOptionResponse{}, nil
}

func (s *benchAgent) SetSessionMode(_ context.Context, p SetSessionModeRequest) (SetSessionModeResponse, error) {
	record(&s.recorder, p)
	return SetSessionModeResponse{}, nil
}

func (s *benchAgent) UnstableArchiveSession(_ context.Context, p UnstableArchiveSessionRequest) (UnstableArchiveSessionResponse, error) {
	record(&s.recorder, p)
	return UnstableArchiveSessionResponse{}, nil
}

func (s *benchAgent) UnstableAuthStatus(_ context.Context, p UnstableAuthStatusRequest) (UnstableAuthStatusResponse, error) {
	record(&s.recorder, p)
	return UnstableAuthStatusResponse{}, nil
}

func (s *benchAgent) UnstableDisableProvider(_ context.Context, p UnstableDisableProviderRequest) (UnstableDisableProviderResponse, error) {
	record(&s.recorder, p)
	return UnstableDisableProviderResponse{}, nil
}

func (s *benchAgent) UnstableForkSession(_ context.Context, p UnstableForkSessionRequest) (UnstableForkSessionResponse, error) {
	record(&s.recorder, p)
	return UnstableForkSessionResponse{}, nil
}

func (s *benchAgent) UnstableListProviders(_ context.Context, p UnstableListProvidersRequest) (UnstableListProvidersResponse, error) {
	record(&s.recorder, p)
	return UnstableListProvidersResponse{}, nil
}

func (s *benchAgent) UnstableMessageMcp(_ context.Context, p UnstableMessageMcpNotification) error {
	record(&s.recorder, p)
	return nil
}

func (s *benchAgent) UnstableSessionOptions(_ context.Context, p UnstableSessionOptionsRequest) (UnstableSessionOptionsResponse, error) {
	record(&s.recorder, p)
	return UnstableSessionOptionsResponse{}, nil
}

func (s *benchAgent) UnstableSessionStatus(_ context.Context, p UnstableSessionStatusRequest) (UnstableSessionStatusResponse, error) {
	record(&s.recorder, p)
	return UnstableSessionStatusResponse{}, nil
}

func (s *benchAgent) UnstableSetProvider(_ context.Context, p UnstableSetProviderRequest) (UnstableSetProviderResponse, error) {
	record(&s.recorder, p)
	return UnstableSetProviderResponse{}, nil
}

func (s *benchAgent) UnstableSetSessionTitle(_ context.Context, p UnstableSetSessionTitleRequest) (UnstableSetSessionTitleResponse, error) {
	record(&s.recorder, p)
	return UnstableSetSessionTitleResponse{}, nil
}

func (s *benchAgent) UnstableUnarchiveSession(_ context.Context, p UnstableUnarchiveSessionRequest) (UnstableUnarchiveSessionResponse, error) {
	record(&s.recorder, p)
	return UnstableUnarchiveSessionResponse{}, nil
}

func (s *benchClient) CompleteElicitation(_ context.Context, p CompleteElicitationNotification) error {
	record(&s.recorder, p)
	return nil
}

func (s *benchClient) CreateElicitation(_ context.Context, p CreateElicitationRequest) (CreateElicitationResponse, error) {
	record(&s.recorder, p)
	return CreateElicitationResponse{}, nil
}

func (s *benchClient) CreateTerminal(_ context.Context, p CreateTerminalRequest) (CreateTerminalResponse, error) {
	record(&s.recorder, p)
	return CreateTerminalResponse{}, nil
}

func (s *benchClient) KillTerminal(_ context.Context, p KillTerminalRequest) (KillTerminalResponse, error) {
	record(&s.recorder, p)
	return KillTerminalResponse{}, nil
}

func (s *benchClient) ReadTextFile(_ context.Context, p ReadTextFileRequest) (ReadTextFileResponse, error) {
	record(&s.recorder, p)
	return ReadTextFileResponse{}, nil
}

func (s *benchClient) ReleaseTerminal(_ context.Context, p ReleaseTerminalRequest) (ReleaseTerminalResponse, error) {
	record(&s.recorder, p)
	return ReleaseTerminalResponse{}, nil
}

func (s *benchClient) RequestPermission(_ context.Context, p RequestPermissionRequest) (RequestPermissionResponse, error) {
	record(&s.recorder, p)
	return RequestPermissionResponse{}, nil
}

func (s *benchClient) SessionUpdate(_ context.Context, p SessionNotification) error {
	record(&s.recorder, p)
	return nil
}

func (s *benchClient) TerminalOutput(_ context.Context, p TerminalOutputRequest) (TerminalOutputResponse, error) {
	record(&s.recorder, p)
	return TerminalOutputResponse{}, nil
}

func (s *benchClient) UnstableAuthStatusUpdate(_ context.Context, p UnstableAuthStatusUpdateNotification) error {
	record(&s.recorder, p)
	return nil
}

func (s *benchClient) UnstableMessageMcp(_ context.Context, p UnstableMessageMcpRequest) (UnstableMessageMcpResponse, error) {
	record(&s.recorder, p)
	return UnstableMessageMcpResponse{}, nil
}

func (s *benchClient) WaitForTerminalExit(_ context.Context, p WaitForTerminalExitRequest) (WaitForTerminalExitResponse, error) {
	record(&s.recorder, p)
	return WaitForTerminalExitResponse{}, nil
}

func (s *benchClient) WriteTextFile(_ context.Context, p WriteTextFileRequest) (WriteTextFileResponse, error) {
	record(&s.recorder, p)
	return WriteTextFileResponse{}, nil
}
