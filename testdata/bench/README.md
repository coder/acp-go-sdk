# Inbound decoding benchmark corpus

`inbound_bench_test.go` measures how fast the SDK reads recorded ACP traffic, the way a `Connection` reads what its peer sends: it decodes the JSON-RPC envelope, then hands a request or notification to the generated dispatcher, which decodes and validates its params, or decodes the result of a response into the type that `SendRequest` returns. The corpus is every message of the files below, read by the side that receives it.

| Folder | Traffic | Source |
| --- | --- | --- |
| `claude-agent-acp/` | What the Claude adapter sends to a client in 40 scenarios | [`agentclientprotocol/claude-agent-acp`](https://github.com/agentclientprotocol/claude-agent-acp) at `1c90fb959a003eba1eed1dac94400c9cdef63195`, `src/tests/acp-scenarios/origin-main/{zed,plain}` |
| `codex-acp/` | What the Codex adapter sends to a client in 33 scenarios | [`agentclientprotocol/codex-acp`](https://github.com/agentclientprotocol/codex-acp) at `ad09e0ead18511a65a0761cac832aa3087219b10`, `src/__tests__/scenarios/data/baseline/{zed,plain}` |
| `g3/` | Both directions of the 15 `verify-acp-go-sdk` journeys between `acpwire` and `example/agent` | `acp-verify wire` on `main` at `6c4769e` |
| `../../.agents/skills/verify-schema-bump/roundtrip/testdata/transcripts/` | Both directions of live sessions with the Claude and Codex adapters (G5) and the TypeScript SDK (G4) | The G2 gate's transcripts, read in place |

Both adapters are licensed under the Apache License 2.0; each folder keeps its `LICENSE`. The files are converted, not copied: each scenario is kept once, from the `zed` client profile when the adapter has it and from `plain` otherwise; the Codex app server's own messages (`codexResponse`) are dropped; and each line becomes an `acpwire` transcript line, `{"dir","msg"}`, with the recorded payload inside a JSON-RPC message. The fixtures record only what the agent sends, so a response names its request's method in `"method"`.

Rebuild the folders with:

```bash
go run ./internal/benchcorpus                         # claude-agent-acp/ and codex-acp/, from GitHub
V=.agents/skills/verify-acp-go-sdk/scripts/acp-verify # g3/: launch a run, then drive every journey
"$V" launch                                           # prints export ACP_VERIFY_RUN=...
for j in .agents/skills/verify-acp-go-sdk/journeys/*.jsonl; do "$V" wire "$(basename "$j")" "$(basename "$j" .jsonl)"; done
go run ./internal/benchcorpus -g3 "$ACP_VERIFY_RUN/evidence"
```

## Benchmarks

| Benchmark | One op |
| --- | --- |
| `BenchmarkInbound/corpus` | Read every corpus message once. |
| `BenchmarkInboundStream/agent_message_chunk`, `/tool_call_update` | Read every `session/update` notification of that kind once: the streamed bursts of a prompt turn. |
| `BenchmarkConnectionPipe/pair` | An `AgentSideConnection` sends every `session/update` notification of the corpus to a `ClientSideConnection` over an `io.Pipe`, with at most 256 sent and not yet handled. |
| `BenchmarkConnectionPipe/inbound` | The same, with the recorded lines written to the `ClientSideConnection` directly, so the agent does not encode them. |

Each reports `cpu-ns/op`, the CPU time that the process used per op (Unix only), and `msgs/op`; the decode benchmarks also report `errors/op`: the messages that the connection rejects or drops, such as a request for a method that the side does not have. `TestInboundCorpus` decodes the corpus once and, with `ACP_BENCH_OUTCOMES=FILE`, writes the outcome of each message: a hash of the decoded value encoded again, or the error.

On a shared machine, compare `cpu-sec/op` rather than `sec/op`: wall time grows while other processes hold the CPU, and CPU time does not. In an A/A run (the same commit on both sides, 10 runs, load average 25 to 40), `sec/op` of `BenchmarkInbound/corpus` varied by up to 82% and `cpu-sec/op` by 11%, with no significant difference on any benchmark.

Run them with `make bench` (`scripts/bench`), which is not part of `make test`. With `BASE=<commit>` it compares that commit with the checkout: it copies the checkout's benchmark file and corpus into `BASE`, checks that both sides decode every message to the same outcome, runs the two test binaries in turn 10 times with `-cpu 1`, and prints `benchstat`. The header lists the corpus digest, so results of two different corpora do not mix.

```bash
make bench BASE=origin/main
RUNS=20 BENCH='^BenchmarkInbound$' scripts/bench origin/main
```

This folder is a Go module of its own (`go.mod`, no code), so the module zip
that the Go proxy serves leaves it out and consumers do not download it. The
corpus tests skip when the folder is missing.
