---
name: verify-schema-bump
description: Offline and canary gates for a PR that bumps the ACP schema version of the Go SDK (schema/version). G1 regenerates the SDK at every upstream schema release between two versions and checks build, vet, method wiring and the exported-API diff; G2 round-trips real adapter transcripts and schema-built messages through the SDK's Go types and checks the x-deserialize-* hints; G7 builds known dependents (docker-agent, jaeger, coder) against the checkout. Use before and during any schema bump, when cmd/generate changes, or to list the API breaks a release note must name.
---

# Verify a schema bump

A schema bump PR changes `schema/version`, regenerates the `*_gen.go` files and fixes the hand-written code. It must prove three things, one gate each:

| Gate | Claim | Script | Network |
| --- | --- | --- | --- |
| G1 | The generator works at every upstream release on the way, the target builds and vets, every method is wired, and the API breaks are known. | `scripts/g1-walk` | Release list, and schema files not yet cached. |
| G2 | The SDK's Go types read and write real and schema-built messages without loss, and honor the schema's `x-deserialize-*` hints. | `scripts/g2-roundtrip` | Only to fill the Go module cache once. |
| G7 | Known dependents still build, or each break is in the release notes. | `scripts/g7-canary` | Clones and Go modules. |

All commands run from the repository root. The scripts need Go and git; G1 also needs `curl` and a logged-in `gh`. On a Coder workspace, set the toolchain first:

```bash
export PATH=$HOME/.local/share/mise/installs/go/1.26.3/bin:$PATH; unset GOROOT
S=.agents/skills/verify-schema-bump/scripts
```

## When to run which

1. Before you start a bump, on `main`: `"$S"/g1-walk TARGET`. The table shows, release by release, where the generated code or the hand-written code stops compiling. Plan the PR from it.
2. On the bump branch, after `make version` and your fixes: `"$S"/g1-walk TARGET`. `--from` defaults to the branch's `schema/version`, so this is one row: the target. It must pass. Copy its list of incompatible API changes into the release notes.
3. On the bump branch: `"$S"/g2-roundtrip`, and the same on the base branch. Compare the two `failures.txt` files (see G2).
4. On the bump branch: `"$S"/g7-canary`. Each group of compile errors must match an entry in the release-note break list.
5. When `cmd/generate` changes without a bump: G1 with `--from` set to the current version and `TARGET` set to the newest release, to see that the change helps at every later release.

## G1: generator walk

```bash
"$S"/g1-walk [--from VERSION] [--ref REF] [--base-tag TAG] TARGET
```

It lists the upstream releases that carry schema files (0.x releases on tags `v<version>`, 1.x and later on `schema-v<version>`, prereleases left out) and keeps those from `--from` to `TARGET`. It adds a throwaway worktree of `--ref` (default `HEAD`). For each release it resets the worktree, copies that release's `meta.json`, `schema.json`, `meta.unstable.json` and `schema.unstable.json` into `schema/`, runs the generator (`cd cmd/generate && env -u GOPATH -u GOMODCACHE go run .`) and `gofmt -w *_gen.go`, and checks:

| Column | Check |
| --- | --- |
| `gen` | The generator exits 0. |
| `idem` | A second generator run leaves the same tree: no file changed, added or removed. |
| `build` | `go build -gcflags=-e ./...` passes. `gen-errs` counts the compile errors in `*_gen.go` files: a generator bug. `hand-errs` counts the rest: hand-written code that the bump must fix. A package that fails hides the errors of the packages that import it. |
| `vet` | `go vet ./...` passes. Runs only when the build passes. |
| `methods` | `methodcheck`: each method in `meta.json` and `meta.unstable.json` has a constant in a `*_gen.go` file and a `case` in the dispatcher of its side, in the `switch` on the method name of `(*AgentSideConnection).handle` or `(*ClientSideConnection).handle`; a method of both sides needs both. A protocol method (`$/...`) needs a constant and a receive path in hand-written code, because `connection.go` dispatches it: a function that decodes a message with `json.Unmarshal` into `x` and compares `x.Method` with the wire string or its constant (`==`, `!=` or `switch x.Method`). Sending it, or comparing another value with it, does not count. |
| `apidiff` | Target row only, when it builds: `apidiff -incompatible` of the package against `--base-tag` (default: the newest `v*` tag merged into `--ref`; with no such tag the script stops and asks for `git fetch --tags origin`). Shows the count; the list follows the table. |

Pass: exit 0 and `G1 PASS`. Every row has `gen=ok` and `idem=ok`, and the target row has `build=ok`, `vet=ok`, `methods=ok` and an API diff. Rows before the target may fail to build: the hand-written code of `--ref` fits only its own schema.

Evidence, in `$VSB_ROOT/runs/g1-FROM-TARGET-TIME/`: `summary.txt` (the table), `versions.txt`, one folder per release with `gen.log`, `idem.diff`, `build.log`, `vet.log`, `methods.txt` and, for the target, `apidiff.txt`; `incompatible.txt` (the release-note list) and `base.export`. Schema files are cached in `$VSB_ROOT/cache/schemas/<tag>/`. `VSB_ROOT` defaults to `${TMPDIR:-/tmp}/verify-schema-bump`. The script removes its worktrees on exit.

## G2: JSON round trip and hints

```bash
"$S"/g2-roundtrip                    # all checks
"$S"/g2-roundtrip -run TestHints     # extra arguments go to go test
```

The test lives in its own Go module (`go.mod` in this folder), so the SDK's module keeps no dependencies. It uses the SDK and `schema/` of the checkout it sits in. `registrygen` first writes `roundtrip/registry_gen_test.go`, a map from each schema def to its SDK Go type (`Unstable<def>` for a def that only the unstable schema has). That file is not committed, so run the test through the script.

The corpus:

- `roundtrip/testdata/transcripts/*.jsonl`: real wire transcripts of the Claude (`claude-*`) and Codex (`codex-*`) ACP adapters, recorded with `acpwire` from the `verify-acp-go-sdk` skill. One line is `{"dir","msg","t_ms"}`. The test finds each message's def from the schema's `x-method` (`<X>Request`, `<X>Response` or `<X>Notification`; `Error` for an error response) and skips extension methods (`_...`) and methods that the schema does not define. `TestCorpusHasNoCredentials` fails on a line that holds a credential-shaped value, such as `sk-...`, `ghp_...` or a bearer token. Drop such a line before you commit a new transcript.
- Values built from every `$defs` entry of both schema files: one with only required properties and one with every property (to depth 5), for each top-level `anyOf`/`oneOf` alternative or enum value. A built value that the schema rejects is a gap in the builder, logged and skipped.

For each value the test decodes it into the def's SDK type, calls its `Validate` method when it has one, encodes it again, and reports one line per problem:

| Token | Meaning |
| --- | --- |
| `NO-TYPE` | A transcript message's def, or any def of `schema.json`, has no SDK type. A def that only `schema.unstable.json` has may lack one: the `SUMMARY instances` line lists it. |
| `DECODE` | The SDK type cannot decode a value. |
| `VALIDATE` | The type's `Validate` method rejects a value. |
| `LOSSY` | The encoded JSON differs from the input. The line lists every differing path and the values at the first. Numbers compare by exact value (`1.0` equals `1`; no digit is rounded away). A member that is `null`, or that equals its property's schema `default`, counts as absent on both sides: the SDK, like the Rust SDK's `#[serde(default)]` fields, reads an absent member as its default and may write the default back. Where the schema gives no default, `{}` and absent differ, because ACP capabilities use `{}` to mean "supported". |
| `SCHEMA` | A schema file accepts the input but rejects the encoded JSON. |
| `HINT` | The SDK ignores an `x-deserialize-*` hint. |
| `UNKNOWN` | Logged, not a failure: the input has a member that no node of the schema defines, such as a field from a newer schema version, and the SDK drops it. Listed in `unknown.txt`. |

`TestHints` finds each property that carries a hint, in a def or in one of its top-level alternatives, in each schema file (a def that both files share is checked once per file, because the files can define it differently), and builds a value of the def around it:

- `x-deserialize-default-on-error`: the value with a good property must be schema-valid, else the site is skipped. Then the property gets the first of `"malformed"`, `12345`, `true`, `{"malformed":true}`, `["malformed"]` that the schema rejects. The SDK must decode it without an error, to the same JSON as the value without the property.
- `x-deserialize-skip-invalid-items`: the array gets one good item and one bad one. The SDK must decode it to the same JSON as the array with the good item only.
- `x-deserialize-default-on-null` (schema 1.27.0 and later): the property is `null`. The SDK must decode it to the same JSON as the value without the property, and must not write the property back as `null` unless it does so for the value without it too.

A schema without hints logs `SUMMARY hints: no hints in this schema`.

Pass: exit 0 and `G2 PASS`. A bump PR whose base already fails compares its `failures.txt` with the base's run (`diff BASE_RUN/failures.txt BUMP_RUN/failures.txt`): a new line is a regression unless the PR explains it, and a line that the bump fixes goes away.

Evidence, in `$VSB_ROOT/runs/g2-VERSION-TIME/`: `test.log` (`go test -v`, with the subtest of each problem), `failures.txt` (one sorted line per distinct problem), `unknown.txt` (one line per distinct `UNKNOWN`) and `summary.txt`.

G2 needs an SDK that compiles. At a schema version where the generated code does not build (see the G1 results below), it cannot run until the bump fixes the build.

## G7: consumer canary

```bash
"$S"/g7-canary                       # docker/docker-agent jaegertracing/jaeger coder/coder
"$S"/g7-canary coder/coder           # or any OWNER/REPO list
```

For each repo it makes or resets a shallow clone of the default branch in `$VSB_ROOT/cache/consumers/`. In each Go module of the repo whose `go.mod` names `github.com/coder/acp-go-sdk`, it runs `go mod edit -replace github.com/coder/acp-go-sdk=<this checkout>` and `go build -gcflags=-e` on the module's packages whose non-test files import the SDK. It prints one line per module and, for a failed build, the compile errors grouped under the SDK symbol that each names (`acp.TerminalId (3)`, or `acp.Type.Field` for a missing field or method). Test files that import the SDK are not built.

Pass: exit 0 and `G7 PASS`. On a bump PR, a failure passes review when each symbol group is a break that the release notes name, with its migration.

Evidence, in `$VSB_ROOT/runs/g7-SHA-TIME/`: `summary.txt` and one build log per module.

## Cleanup

The scripts remove their worktrees and build folders on exit and keep the evidence in `$VSB_ROOT/runs/`. The caches in `$VSB_ROOT/cache/` make later runs faster. Delete `$VSB_ROOT` to drop all of it. The consumer clones hold a `go.mod` edit until the next G7 run resets them.

## Not in `make test`

None of the gates runs in `make test`. G1 and G7 need the network. G2 needs the skill module's dependencies in the module cache, and it fails at schema 0.13.5 (below), so it would turn CI red. When G2 passes at the current schema, add `cd .agents/skills/verify-schema-bump && scripts/g2-roundtrip` to `make test`: it takes about a second once the cache is warm.

## Known results at schema 0.13.5

`main` at `492745b`, measured on 2026-11-17.

G1 at 0.13.5 alone (`g1-walk 0.13.5`): `gen`, `idem` and `build` pass; `vet` fails with 12 `unreachable code` findings in `types_gen.go`; `methods` reports 3 gaps: `mcp/message` (`x-side: both`) has no dispatcher case on either side, and `$/cancel_request` has no generated constant. The API diff against `v0.13.5` is empty.

G1 from 0.13.5 to 1.32.0 (`g1-walk 1.32.0`, 25 releases): every release generates and regenerates the same output. The build fails from 0.13.6 on:

| Releases | Generated code | Hand-written code |
| --- | --- | --- |
| 0.13.6, 1.13.7 | builds | `example/agent` no longer satisfies `acp.Agent` (5 errors) |
| 1.14.0 to 1.17.0 | builds | `helpers.go:71`: a `string` where `TerminalId` is now required |
| 1.18.0 to 1.32.0 | `types_gen.go`: `v.Message undefined` in `Validate` of `CreateElicitationRequest` (`UnstableCreateElicitationRequest` at 1.18.0 to 1.20.0) | `helpers.go:71` as above |

From 1.24.0 on, `methods` reports only the `$/cancel_request` constant. `apidiff` at 1.32.0 is skipped because the target does not build.

G2 (`g2-roundtrip`): 242 of 255 defs have an SDK type; the 13 without one are unstable-only elicitation schema defs, listed, not failed. Every stable def at every bump target (1.13.7, 1.20.0, 1.21.0, 1.25.0, 1.32.0) has one. 879 built values and 51 transcript messages are checked (8 extension messages and 2 `auth/status_update` notifications skipped). G2 fails with 183 distinct problems:

- `HINT` 165 (51 in `schema.json`, 114 in `schema.unstable.json`): the SDK honors no hint. 122 `x-deserialize-default-on-error` and 43 `x-deserialize-skip-invalid-items` sites fail to decode a bad value.
- `LOSSY` 14: `ContentBlock` drops `_meta` and `annotations` (6 lines: also `Content`, `ContentChunk`, `ToolCallContent`, `PromptRequest.prompt` and `SessionUpdate`, which hold one); the unstable elicitation types drop `sessionId`, `toolCallId`, `elicitationId`, `url` and `requestedSchema` (5 lines); the SDK writes the unstable `auth: {}` capability into `ClientCapabilities` built from `schema.json`, which does not define it (3 lines).
- `SCHEMA` 4: the encoded `CreateElicitationRequest`, `ElicitationFormMode` and `ElicitationUrlMode` miss required properties.
- `UNKNOWN` 3 (not failures): the adapters send fields that 0.13.5 does not define, and the SDK drops them: `agentCapabilities.auth.status`, `agentCapabilities.sessionCapabilities.options`, `.status`, `.systemPrompt` and `.title`, and `NewSessionResponse.models`.

G7 (`g7-canary`): passes. `docker/docker-agent` at `19a7356` (1 package), `jaegertracing/jaeger` at `f141f57` (3 packages) and `coder/coder` at `a3f25a4` (2 packages: `agent/x/agentacp`, `coderd/x/chatd`) build against `main`. A checkout with `NewInvalidParams` and `PromptResponse.StopReason` renamed fails docker-agent with two groups, `acp.NewInvalidParams (29)` and `acp.PromptResponse.StopReason (5)`.
