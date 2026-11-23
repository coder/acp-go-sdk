---
name: verify-schema-bump
description: Offline and canary gates for a PR that bumps the ACP schema version of the Go SDK (schema/version). G1 regenerates the SDK at every upstream schema release between two versions and checks build, vet, method wiring and the exported-API diff; G2 round-trips real adapter transcripts and schema-built messages through the SDK's Go types and checks the x-deserialize-* hints; G7 builds known dependents (docker-agent, jaeger, coder) against the checkout; the Rust probe reads the same values with the Rust SDK's schema crate and lists where the two SDKs differ. Use before and during any schema bump, when cmd/generate changes, or to list the API breaks a release note must name.
---

# Verify a schema bump

A schema bump PR changes `schema/version`, regenerates the `*_gen.go` files and fixes the hand-written code. It must prove three things, one gate each:

| Gate | Claim | Script | Network |
| --- | --- | --- | --- |
| G1 | The generator works at every upstream release on the way, the target builds and vets, every method is wired, and the API breaks are known. | `scripts/g1-walk` | Release list, and schema files not yet cached. |
| G2 | The SDK's Go types read and write real and schema-built messages without loss, and honor the schema's `x-deserialize-*` hints. | `scripts/g2-roundtrip` | Only to fill the Go module cache once. |
| G7 | Known dependents still build, or each break is in the release notes. | `scripts/g7-canary` | Clones and Go modules. |
| Rust | The SDK reads each value as the Rust crate `agent-client-protocol-schema` does. | `scripts/rust-diff` | crates.io for the first build, and the upstream tag list. |

All commands run from the repository root. The scripts need Go and git; G1 also needs `curl` and a logged-in `gh`, and the Rust probe needs `cargo`. On a Coder workspace, set the toolchain first (the mise `cargo` shim can fail there, so name rustup's):

```bash
export PATH=$HOME/.local/share/mise/installs/go/1.26.3/bin:$PATH; unset GOROOT
export CARGO=$HOME/.cargo/bin/cargo
S=.agents/skills/verify-schema-bump/scripts
```

## When to run which

1. Before you start a bump, on `main`: `"$S"/g1-walk TARGET`. The table shows, release by release, where the generated code or the hand-written code stops compiling. Plan the PR from it.
2. On the bump branch, after `make version` and your fixes: `"$S"/g1-walk TARGET`. `--from` defaults to the branch's `schema/version`, so this is one row: the target. It must pass. Copy its list of incompatible API changes into the release notes.
3. On the bump branch: `"$S"/g2-roundtrip`, and the same on the base branch. Compare the two `failures.txt` files (see G2).
4. On the bump branch: `"$S"/g7-canary`. Each group of compile errors must match an entry in the release-note break list.
5. When `cmd/generate` changes without a bump: G1 with `--from` set to the current version and `TARGET` set to the newest release, to see that the change helps at every later release.
6. When a PR changes how the SDK decodes or encodes a value (a hint, a union, a number type), and on a bump branch after the crate pin moves: `"$S"/rust-diff`, then again with `RUST_CASES` set to a `G2_PROBES` file. Compare its `details.txt` with the base's.

## G1: generator walk

```bash
"$S"/g1-walk [--from VERSION] [--ref REF] [--base-tag TAG] TARGET
```

It lists the upstream releases that carry schema files (0.x releases on tags `v<version>`, 1.x and later on `schema-v<version>`, prereleases left out) and keeps those from `--from` to `TARGET`. It adds a throwaway worktree of `--ref` (default `HEAD`). For each release it resets the worktree, deletes the generated files (`*_gen.go` and `*_gen_test.go`), copies that release's `meta.json`, `schema.json`, `meta.unstable.json` and `schema.unstable.json` into `schema/`, runs the generator (`cd cmd/generate && env -u GOPATH -u GOMODCACHE go run .`) and `gofmt -w` on the generated files, and checks:

| Column | Check |
| --- | --- |
| `gen` | The generator exits 0. |
| `idem` | A second generator run leaves the same tree: no file changed, added or removed. |
| `drift` | Only in the row of the ref's own `schema/version`: the ref's committed `schema/` files and generated files equal the downloaded schema and the regenerated code (`drift.diff` lists the files that differ). A bump PR that forgot to commit `make version` output fails here. `-` in other rows. |
| `build` | `go build -gcflags=-e ./...` passes. `gen-errs` counts the compile errors in `*_gen.go` files: a generator bug. `hand-errs` counts the rest: hand-written code that the bump must fix. A package that fails hides the errors of the packages that import it. |
| `vet` | `go vet ./...` passes. Runs only when the build passes. |
| `methods` | `methodcheck`: each method in `meta.json` and `meta.unstable.json` has a constant in a `*_gen.go` file and a `case` in the dispatcher of its side, in the `switch` on the method name of `(*AgentSideConnection).handle` or `(*ClientSideConnection).handle`; a method of both sides needs both. A proxy method (`proxyMethods`: `proxy/initialize`, `proxy/successor`) needs a constant and a `case` in `(*UnstableProxySideConnection).handle`. A protocol method (`$/...`) needs a constant and a receive path in hand-written code, because `connection.go` dispatches it: a function that decodes a message with `json.Unmarshal` into `x` and then compares that same variable's `x.Method` with the wire string or its constant (`==`, `!=` or `switch x.Method`). Sending it, or comparing another value with it, does not count. |
| `apidiff` | Target row only, when it builds: `apidiff -incompatible` of the package against `--base-tag` (default: the newest `v*` tag merged into `--ref`; with no such tag the script stops and asks for `git fetch --tags origin`). Shows the count; the list follows the table. |

Pass: exit 0 and `G1 PASS`. Every row has `gen=ok` and `idem=ok` and no `drift=FAIL`, and the target row has `build=ok`, `vet=ok`, `methods=ok` and an API diff. Rows before the target may fail to build: the hand-written code of `--ref` fits only its own schema. The API diff must not change `Agent` or `Client`: every agent and client implements one of them, so the script prints such lines under `Agent or Client changed` and fails. Their methods are frozen in `cmd/generate/internal/ir/baseline.go`, and `TestAgentAndClient_KeepTheirMethods` pins them in `make test`.

Since #128 the generator fails when merging the unstable schema changes a stable Go type, and the panic lists each change (`cmd/generate/internal/stablecheck`). So `gen` fails by design at 0.13.5 to 1.17.0, whose unstable `SetSessionConfigOptionRequest` moves the required `value` into `anyOf` alternatives (`SetSessionConfigOptionRequest.value: removed`), and a walk with `--from` below 1.18.0 fails G1.

Every other method has its own optional interface, named after the side and the Go method. A bump that makes a method stable renames its interface and its method, for example `AgentUnstableForkSession` with `UnstableForkSession` to `AgentForkSession` with `ForkSession`, and the API diff lists the old interface as removed. An implementer that does not assert the old interface keeps compiling, and its peer then gets `-32601` for the method. The release notes must name each such rename, with the new interface and method.

Evidence, in `$VSB_ROOT/runs/g1-FROM-TARGET-TIME/`: `summary.txt` (the table), `versions.txt`, one folder per release with `gen.log`, `idem.diff`, `drift.diff` (the ref's own version), `build.log`, `vet.log`, `methods.txt` and, for the target, `apidiff.txt`; `incompatible.txt` (the release-note list) and `base.export`. Schema files are cached in `$VSB_ROOT/cache/schemas/<tag>/`. `VSB_ROOT` defaults to `${TMPDIR:-/tmp}/verify-schema-bump`. The script removes its worktrees on exit.

## G2: JSON round trip and hints

```bash
"$S"/g2-roundtrip                    # all checks
"$S"/g2-roundtrip -run TestHints     # extra arguments go to go test
```

The test lives in its own Go module (`go.mod` in this folder), so the SDK's module keeps no dependencies. It uses the SDK and `schema/` of the checkout it sits in. `registrygen` first writes `roundtrip/registry_gen_test.go`, a map from each schema def to its SDK Go type (`Unstable<def>` for a def that only the unstable schema has). That file is not committed, so run the test through the script.

The corpus:

- `roundtrip/testdata/transcripts/*.jsonl`: real wire transcripts, one line `{"dir","msg","t_ms"}` each. `claude-1`, `claude-2`, `codex-1` and `codex-2` are short sessions with the Claude and Codex ACP adapters, recorded with `acpwire` from the `verify-acp-go-sdk` skill. The rest come from the `verify-real-harnesses` runs of 2026-11-18 at schema 1.32.0, one file per connection (`conn`), because each connection numbers its requests from the start: `claude-g5-*` (claude-agent-acp 0.108.0) and `codex-g5-*` (codex-acp 2.7.0) are every G5 scenario, `ts-agent-g4-1` is `acpharness` against the TypeScript SDK 2.7.0 example agent, and `ts-client-g4` is the TypeScript SDK client against `example/agent`. `scripts/harness scan` found no credential value and no key-shaped string in their evidence before they were copied. The test finds each message's def from the schema's `x-method` (`<X>Request`, `<X>Response` or `<X>Notification`; `Error` for an error response) and skips extension methods (`_...`) and methods that the schema does not define. `TestCorpusHasNoCredentials` fails on a line that holds a credential-shaped value, such as `sk-...`, `ghp_...` or a bearer token. Drop such a line before you commit a new transcript.
- Values built from every `$defs` entry of both schema files: one with only required properties and one with every property (to depth 5), for each top-level `anyOf`/`oneOf` alternative or enum value. In the value with every property, an object whose schema says `"additionalProperties": true` or `"unevaluatedProperties": true` also gets the member `example` (or `example2` and so on, when a property has that name), which no property names. That member, and each value that the schema leaves free, such as `rawInput`, is the integer `9007199254740993` (2^53 + 1), so an SDK that decodes it into a `float64` shows `LOSSY`. A built value that the schema rejects is a gap in the builder, logged and skipped.

For each value the test decodes it into the def's SDK type, calls its `Validate` method when it has one, encodes it again, and reports one line per problem:

| Token | Meaning |
| --- | --- |
| `NO-TYPE` | A transcript message's def, or any def of `schema.json`, has no SDK type. For a `schema.json` def only a type of the same name counts, not `Unstable<def>`. A def that only `schema.unstable.json` has may lack one: the `SUMMARY instances` line lists it. |
| `DECODE` | The SDK type cannot decode a value. |
| `VALIDATE` | The type's `Validate` method rejects a value. |
| `LOSSY` | The encoded JSON differs from the input. The line lists every differing path and the values at the first. Numbers compare by exact value (`1.0` equals `1`; no digit is rounded away). A member that is `null`, or that equals the `default` of its property in every schema file that defines the property, counts as absent on both sides: the SDK, like the Rust SDK's `#[serde(default)]` fields, reads an absent member as its default and may write the default back. Where the schema gives no default, `{}` and absent differ, because ACP capabilities use `{}` to mean "supported". |
| `SCHEMA` | A schema file accepts the input but rejects the encoded JSON. |
| `HINT` | The SDK ignores an `x-deserialize-*` hint. |
| `PATCH` | The SDK does not keep the three forms of a patch field apart: absent, `null`, and, for an array, `[]` (see `TestPatches`). |
| `UNKNOWN` | Logged, not a failure: the input has a member that no node of the schema defines, such as a field from a newer schema version, and the SDK drops it. Listed in `unknown.txt`. A node with `"additionalProperties": true` or `"unevaluatedProperties": true` defines every member, so a member that the SDK drops there is `LOSSY`. A catch-all variant whose `not` matches the value does not describe it, so an undefined member of a known `form` mode stays `UNKNOWN`. An open variant counts only when the value has its required members, so an open sibling that a `required` member tells apart does not turn an undefined member of a closed variant into `LOSSY`. |

`TestHints` finds each property that carries a hint in each schema file (a def that both files share is checked once per file, because the files can define it differently). It looks in each def, in each top-level alternative of the def, and in the items of an alternative that is an array, and it follows `$ref` and `allOf`. So `args` of `McpServerStdio` is checked in that type and again in `McpServer`, the union that must select the stdio variant before the SDK can honor the hint. The test builds a value of the def around the property:

- `x-deserialize-default-on-error`: the value with a good property must be schema-valid, else the site is skipped. Then the property gets two bad values in turn, each only when the schema rejects it: `null`, and the first of `"malformed"`, `12345`, `true`, `{"malformed":true}`, `["malformed"]`. The SDK must decode each without an error, to the same JSON as the value without the property.
- A property whose type is a closed enum with the member `other`, such as `ToolKind`, never gets a string as the bad value. The Rust types mark that member `#[serde(other)]` and read every string that the enum does not list as `other`, and so does the SDK. The schema does not say so, which is why the test goes by the member's name.
- `x-deserialize-skip-invalid-items`: the array gets one good item and one bad one, chosen the same way. The SDK must decode it to the same JSON as the array with the good item only.
- Deep probes, for `x-deserialize-default-on-error` (unless the property also has `x-deserialize-skip-invalid-items`) and for the items of `x-deserialize-skip-invalid-items`: the test builds a value with every property (to depth 5) and breaks one rule inside it at a time. `missing-required` drops a member, `null-member` sets a member or an item to `null`, and `unknown-enum` sets a string to `x-unknown-value`. It does not change a member whose own hint tolerates the change, or look into it, but it does drop it: a required member must be there all the same. It keeps only the changes that the schema rejects. The value still decodes into the SDK's Go type, but the Rust types reject it, so the SDK must read it as absent, or drop the item, as for a value of the wrong type. A string of an enum with the member `other` is never changed.
- `x-deserialize-default-on-null` (schema 1.27.0 and later): the property is `null`. The SDK must decode it to the same JSON as the value without the property.
- A property that is `null` in the input must not come back as `null`, unless the SDK writes it so for the value without the property too.
- For a required array, the value without the property is the value with an empty array instead. A union selects no variant without its required keys, and the Rust types read the bad value as an empty array. Every required property with a hint is an array in the 25 schema releases up to 1.32.0.

Each `HINT` line names the probe class in brackets: `null`, `wrong-type`, `missing-required`, `null-member` or `unknown-enum`. A class with more than one bad value reports how many failed and the first. With `G2_PROBES=FILE`, `TestHints` writes every bad value that it tries to `FILE`, one JSON object per line (`def`, `file`, `site`, `hint`, `probe`, `at`, the input in `json` and the value without the bad part in `want`), so that another SDK, such as the Rust types, can decode the same values.

A schema without hints logs `SUMMARY hints: no hints in this schema`.

`TestPatches` finds each patch field: an optional property of an update that changes a value the receiver stores, where absent means unchanged, `null` clears it and a value replaces it. The schema says so only in its descriptions, so the test reads them as `cmd/generate/internal/ir` does (see its README): an object whose description says that its optional properties are all patch fields, an object that names them in a "have patch semantics" sentence, and a property whose description says that omitted means unchanged or that `null` clears it. It looks in each def and each top-level alternative, following `$ref` and `allOf`, so the `tool_call_update` variant of `SessionUpdate` is checked too. For each one it builds the def's value without the property, with `null`, and, for an array, with `[]`, keeps the forms that the schema accepts, and checks that the SDK writes the member back the same: absent, `null` or `[]`. `null` clears only where the text says so: in an object that says only that it carries the changed fields, as `ToolCallUpdate` and `SessionInfoUpdate` do, a property whose description does not say that `null` clears it reads `null` as unchanged, as ACP v1 and the Rust SDK read it, so there `null` must come back absent. `LOSSY` cannot see this, because it counts `null` as absent. At schema 1.32.0 it finds 86 sites; 40 of them read `null` as absent, and an SDK that keeps `null` there, as v0.20.0 does, fails those 40.

Each problem line is `TOKEN KEY: DETAIL`. The key names the def and the property or path that the problem is about: the field of a `DECODE` type error, the property of a `VALIDATE` "is required" error, every differing path of a `LOSSY` value, the location of the first failing rule of a `SCHEMA` value, and the hint, site and schema file of a `HINT` line. The key never holds the error text, so a changed message or value keeps it. `failures.txt` lists each key with the number of distinct lines that have it, such as `LOSSY SessionNotification /update/content (1)`. A new kind of failure at a key that the base already has, such as a different value lost at the same path, raises that number.

Pass: exit 0 and `G2 PASS`. A bump PR whose base already fails compares its `failures.txt` with the base's run (`diff BASE_RUN/failures.txt BUMP_RUN/failures.txt`): a new key or a higher count is a regression unless the PR explains it, and a key that the bump fixes goes away. `details.txt` has the lines of each key.

Evidence, in `$VSB_ROOT/runs/g2-VERSION-TIME/`: `test.log` (`go test -v`, with the subtest of each problem), `failures.txt` (one sorted key per distinct problem, with its count of distinct lines), `details.txt` (the whole line of each problem), `unknown.txt` (one key per distinct `UNKNOWN`) and `summary.txt`.

G2 needs an SDK that compiles. At a schema version where the SDK does not build (see the G1 results below), it cannot run until the bump fixes the build.

## Rust probe

```bash
"$S"/rust-diff                                    # the committed cases
RUST_CASES=$PROBES "$S"/rust-diff                 # any cases file, such as a G2_PROBES file
```

The Rust SDK reads ACP messages with the types of the crate `agent-client-protocol-schema`, which upstream generates the schema from. `rustprobe/` is a small cargo program on that crate (feature `unstable`). Its `build.rs` writes one match arm per `$defs` name of the checkout's `schema/schema.json` and `schema/schema.unstable.json`, so each def decodes as the crate type of that name (`ProtocolVersion` is at the crate root, the rest in `v1`). The JSON-RPC envelope defs `AgentRequest`, `AgentResponse`, `AgentNotification`, their `Client` and `Proxy` twins, decode as the crate's envelope of the payload enum of that name, such as `v1::Response<v1::AgentResponse>`, because the crate names each envelope after its payload. It reads one case per line on stdin, `{"id","def","json"}`, and writes `{"id","ok":true,"out"}` with the value encoded again, or `{"id","ok":false,"err"}`. A def that the crate does not have fails the build.

The crate versions apart from the schema: 1.18.0 is schema 1.32.0. The script first checks the pin. The crate's `.cargo_vcs_info.json` in the cargo registry names the upstream commit that it was published from, and that commit must be the one that the `schema-v<schema/version>` tag points to (`git ls-remote https://github.com/agentclientprotocol/agent-client-protocol refs/tags/schema-v1.32.0`). On a mismatch it prints both commits and stops. A bump moves the pin by hand: find the crate release whose tag `v<crate>` points at the same commit as `schema-v<new>`, set it in `.agents/skills/verify-schema-bump/rustprobe/Cargo.toml`, and run `cargo update -p agent-client-protocol-schema --manifest-path .agents/skills/verify-schema-bump/rustprobe/Cargo.toml` to refresh the committed `Cargo.lock`.

Then it builds the probe with `cargo build --release --locked` in `$VSB_ROOT/cache/rustprobe` and runs `TestRust`, which reads each case with the SDK as the connection reads a message (decode; then `Validate` when the type has it and the def is a request or a notification, because the dispatcher validates those and `Connection.SendRequest` does not validate a result; then encode). It reads the def's generated type, so it does not model the two messages that `connection.go` reads with hand-written code: an error response, which it reads as `RequestError` with every field optional, and `$/cancel_request`, whose `requestId` it reads as raw JSON (so it takes `1.5`, which the generated `RequestId` rejects). Cases of `Error` and `CancelRequestNotification` compare the generated types only and with the probe. It sorts each case into one verdict:

| Verdict | Meaning |
| --- | --- |
| `same` | Both accept it and write the same JSON. |
| `both-reject` | Both reject it. |
| `differs` | Both accept it and write different JSON. The line lists every differing path, with the Go and the Rust value. |
| `go-only` | The SDK accepts it and Rust rejects it. The SDK is the more lenient reader. |
| `rust-only` | Rust accepts it and the SDK rejects it. |
| `no-type` | One side has no type for the def. |

Before it compares the two outputs, it normalizes both as G2's `LOSSY` check does: a member that equals its property's schema default counts as absent, because Rust writes `"loadSession": false` where the SDK leaves the member out. Unlike G2, `null` and absent differ, because a patch field reads `null` as "clear" and an absent member as "unchanged", and an integer literal differs from a number literal of the same value (`2` against `2.0`): a reader that picks a union variant by the JSON type, such as `ElicitationContentValue`, reads them differently.

Each case that does not agree is one line, `RUST VERDICT DEF ID: DETAIL`, such as `RUST differs CreateElicitationResponse 125/elicitation-number-2.0: 1 difference(s): /content/n: go=2 rust=2.0`. Pass: exit 0 and `RUST PASS`, every case `same` or `both-reject`. Every committed case agrees since the strict-decoding PRs (#133, #134, #138), so `rust-diff` passes on `main`. A run that exits 1 has a regression or a case for a new difference: compare its `details.txt` with the base's run, and a new line is a regression.

`.agents/skills/verify-schema-bump/roundtrip/testdata/rust/cases.jsonl` holds one case per known difference, named after its issue (`125/...`), the controls (`control/...`), among them the seven cases of #82 and #125 that #133, #134 and #138 fixed, the five `141/...` cases of a `null` that leaves a `tool_call_update` field or `_meta` of a `session_info_update` unchanged, the `dup/...` cases of #141, each an object with a member twice, and one `envelope/...` case per Agent and Client envelope def. The crate's payload enums inside an envelope are untagged: they ignore `method` and take the first variant that the params or result fit. A `session/prompt` request in `ClientRequest` reads as a request with no members, and so does a `PromptResponse` result in `AgentResponse`. So each envelope case carries a payload that its first fitting variant reads whole, and a case about a payload belongs on its own def, such as `PromptRequest`. The case should leave the file, or move to the controls, when the PR that fixes it lands. With a `G2_PROBES` file, which has no `id`, a case is named `FILE:LINE`.

Evidence, in `$VSB_ROOT/runs/rust-VERSION-TIME/`: `summary.txt` (the pin check and the counts), `details.txt` (one `RUST` line per case that does not agree) and `test.log`.

## G7: consumer canary

```bash
"$S"/g7-canary                       # docker/docker-agent jaegertracing/jaeger coder/coder
"$S"/g7-canary coder/coder           # or any OWNER/REPO list
```

For each repo it makes or resets a shallow clone of the default branch in `$VSB_ROOT/cache/consumers/`. In each Go module of the repo whose `go.mod` names `github.com/coder/acp-go-sdk`, it runs `go mod edit -replace github.com/coder/acp-go-sdk=<this checkout>` and `GOWORK=off go build -gcflags=-e` on the module's packages whose non-test files import the SDK. It prints one line per module and, for a failed build, the compile errors grouped under the SDK symbol that each names (`acp.TerminalId (3)`, or `acp.Type.Field` for a missing field or method). Test files that import the SDK are not built.

Pass: exit 0 and `G7 PASS`. On a bump PR, a failure passes review when each symbol group is a break that the release notes name, with its migration.

Evidence, in `$VSB_ROOT/runs/g7-SHA-TIME/`: `summary.txt` and one build log per module.

## Cleanup

The scripts remove their worktrees and build folders on exit and keep the evidence in `$VSB_ROOT/runs/`. The caches in `$VSB_ROOT/cache/` make later runs faster. Delete `$VSB_ROOT` to drop all of it. The consumer clones hold a `go.mod` edit until the next G7 run resets them.

## Not in `make test`

None of the gates runs in `make test`. G1, G7 and the Rust probe need the network, and the Rust probe needs cargo: `TestRust` skips itself unless `RUST_PROBE` names the probe binary. G2's data checks (`TestTranscripts`, `TestInstances`, `TestHints`) still fail with the accepted lines below, so they would turn CI red. `make test` runs the module's unit tests instead: it writes the registry with `registrygen`, then runs `go vet ./...` and `go test -skip '^(TestTranscripts|TestInstances|TestHints)$' ./...` in this folder, which covers `methodcheck`, `TestPatches` and the roundtrip helpers (`TestCompareJSON`, `TestFindHints_*`, `TestCheckHint`, `TestRoute`, `TestCorpusHasNoCredentials`) and the Rust probe's Go side (`TestCompareSides`, `TestReadCases`, and `TestRustCases_NameSDKTypes`, which checks that each committed case names a def that the SDK has a type for). A new test runs there unless the skip list names it. When G2 passes at the current schema, replace those lines with `cd .agents/skills/verify-schema-bump && scripts/g2-roundtrip`: it takes about a second once the cache is warm.

## Known results at schema 0.13.5

Measured on 2026-11-17. G1 and G7: `main` at `a0c38c3` (after #90) with this skill merged. G2: after #93, #94, #95 and #97, which fixed the decode problems that G2 found.

G1 at 0.13.5 alone (`g1-walk 0.13.5`): `gen`, `idem`, `drift`, `build` and `vet` pass; `methods` reports 2 gaps: `mcp/message` (`x-side: both`) has no dispatcher case on either side. The API diff against `v0.13.5` is empty.

G1 from 0.13.5 to 1.32.0 (`g1-walk 1.32.0`, 25 releases), before #128: every release generates and regenerates the same output. The build fails from 0.13.6 on:

| Releases | Generated code | Hand-written code |
| --- | --- | --- |
| 0.13.6, 1.13.7 | builds | `example/agent` no longer satisfies `acp.Agent` (5 errors) |
| 1.14.0 to 1.32.0 | builds | `helpers.go:71`: a `string` where `TerminalId` is now required |

From 1.24.0 on, `methods` passes: the generator dispatches `mcp/message` on both sides. `apidiff` at 1.32.0 is skipped because the target does not build. The `helpers.go:71` failure hides later errors: with it fixed, 1.32.0 has 31 compile errors in `example/` and `go vet` fails on `acp_test.go`.

G2 (`g2-roundtrip`): 242 of 255 defs have an SDK type; the 13 without one are unstable-only elicitation schema defs, listed, not failed. Every stable def at every bump target (1.13.7, 1.20.0, 1.21.0, 1.25.0, 1.32.0) has one. 879 built values and 51 transcript messages are checked (8 extension messages and 2 `auth/status_update` notifications skipped). All 211 hint sites pass (72 in `schema.json`, 139 in `schema.unstable.json`). 46 of them are a property of a union alternative. G2 still fails, with 3 distinct problems:

- `LOSSY` 3: the SDK writes the unstable `auth: {}` capability into `ClientCapabilities` built from `schema.json`, which does not define it (`ClientCapabilities` once and `InitializeRequest` twice). These lines are accepted. They go away at schema 1.21.0, where `auth` becomes stable.
- `UNKNOWN` 3 (not failures): the adapters send fields that 0.13.5 does not define, and the SDK drops them: `agentCapabilities.auth.status`, `agentCapabilities.sessionCapabilities.options`, `.status`, `.systemPrompt` and `.title`, and `NewSessionResponse.models`.

At schema 1.21.0 (`449d328`, 2026-11-18), where elicitation and `auth` are stable, all 265 defs have an SDK type, 1077 built values and the same 51 transcript messages are checked, and 895 of 919 hint sites pass (24 skipped, 0 fail). The three `auth: {}` lines are gone. One `LOSSY` line is new and accepted: the SDK drops an empty `env: {}` of `AuthMethodTerminal` in an `InitializeResponse`, as the Rust SDK does. The `UNKNOWN` lines are the same.

At schema 1.25.0 (`873c6eb`, 2026-11-18), G1 from 1.21.0 passes: every release generates the same output twice, 1.21.0 to 1.23.0 still miss the `mcp/message` cases (2 gaps each), and from 1.24.0 on `methods` passes, so the long-accepted gap is closed. The target row has `drift`, `build`, `vet` and `methods` ok and lists 17 incompatible changes against `v0.16.0`, all in the unstable MCP-over-ACP API, the two experimental interfaces and `SchemaVersion`. G2 matches 1.21.0: the same accepted `LOSSY` line (`env: {}` of `AuthMethodTerminal`) and the same `UNKNOWN` lines, with all 277 defs typed, 1152 built values and 910 of 928 hint sites passing (18 skipped, 0 fail). Schema 1.25.0 removes the default-on-error hints from 14 properties of `schema.json` that a receiver acts on, such as `CreateTerminalRequest.args` and `NewSessionRequest.mcpServers`, so those values now fail to decode. G7 reports the same compile-error groups as against `v0.16.0`: no consumer uses an API that v0.17.0 breaks.

With the optional interfaces of B5a (`4b34603`, 2026-11-18, schema still 1.25.0), before #128, G1 from 0.13.5 to 1.32.0 generates every release twice with the same output, and the generated code builds at all 25. `Agent` and `Client` keep the methods of 1.25.0 at every later release: the API diff at 1.32.0 against `v0.17.0` lists 127 incompatible changes and adds no method to either interface. From 1.26.0 on, the hand-written code builds too, where `main` fails with 19 or 20 errors in `example/agent`, and only `go vet` fails, on the test agent in `acp_test.go`, which still implements the next-edit-suggestion methods that 1.26.0 removes.

At schema 1.32.0 (`4b9d5da`, 2026-11-18), G1 from 1.25.0 passes: every release generates the same output twice and builds, and the target row has `drift`, `build`, `vet` and `methods` ok and lists 117 incompatible changes against `v0.17.0`. They are the experimental interfaces, the next edit suggestions that 1.26.0 removes, two unstable plan and subagent changes, the type of the unstable fork response's config options, and `SchemaVersion`. `Agent` and `Client` are unchanged. From 1.27.0 on, the unstable schema's descriptions link to the draft docs, which made the merge copy about 30 types that differ only in text, such as `UnstableSessionId` for `session/fork`. The merge now compares definitions by shape. G2: the same accepted `LOSSY` line as at 1.25.0, and one `UNKNOWN` line (`models` of `NewSessionResponse`). 254 of 262 defs have an SDK type. The 8 without one are the unstable `proxy/initialize` and `proxy/successor` types, which belong to the proxy role that the SDK does not have. 1118 built values and 53 transcript messages are checked, the 2 `auth/status_update` notifications included, and 938 of 966 hint sites pass (28 skipped, 0 fail). G7 reports the same compile-error groups as against `v0.17.0`.

With the proxy role (2026-11-20, schema 1.32.0), G1 from 1.18.0 to 1.32.0 and from 1.32.0 to 1.33.0 passes, and `methods` checks the two proxy methods too. Releases before 1.29.0 list no `proxyMethods`, so the generator writes no `proxy_gen.go` there and the hand-written `proxy.go` does not build (`undefined: UnstableProxyFromClient`). From 1.29.0 on every release builds. 1.29.0 to 1.30.0 fail `vet` on `empty_optional_test.go`, which refers to a type that those releases lack.

With the deep probes (#96, 2026-11-18, schema 1.32.0), `TestHints` tries 1669 bad values at the 938 sites it does not skip, 576 of them deep: 253 `missing-required`, 261 `null-member` and 62 `unknown-enum`. `main` at `020bcaf` (v0.18.0) fails 81 sites with 168 lines: 81 `missing-required`, 81 `null-member` and 6 `unknown-enum`, such as a `SessionInfo` without `cwd` in `ListSessionsResponse.sessions`, `agentInfo` without `version`, and a `PlanEntry` with an unknown `priority`. The SDK that checks hinted values against their shapes passes all 938. The Rust crate `agent-client-protocol-schema` 1.18.0 (schema 1.32.0, feature `unstable`) reads each of the 1669 bad values the same as the value without the bad part.

With the harness transcripts in the corpus (2026-11-18, schema 1.32.0, `main` at `020bcaf`), G2 checks 509 transcript messages (19 extension messages skipped) and reports 3 keys, all `LOSSY` with a count of 1:

- `LOSSY InitializeResponse /authMethods/0/env`: the accepted `env: {}` line above.
- `LOSSY SessionNotification /update/content` and `LOSSY SessionNotification /update/content /update/locations`: Claude sends `content: []` and `locations: []` in a full `tool_call` update (the `ToolCall` variant, not a `tool_call_update` patch), and the SDK leaves the empty arrays out when it encodes it. The Rust crate `agent-client-protocol-schema` 1.18.0 leaves them out of a full `tool_call` too. Both keys are accepted: in a full `tool_call` an empty array means the same as an absent one. #106 is related: it covers the same arrays in a `tool_call_update` patch, where `[]` means "clear" and the Rust crate keeps them but the SDK drops them.

`unknown.txt` lists 5 keys: `models` of the new, load and resume session responses, and `sessionId` of the load and resume session responses, which Claude sends and schema 1.32.0 does not define.

With unnamed members in the built values (#102, 2026-11-18, schema 1.32.0), G2 checks 1124 built values. `main` at `020bcaf` (v0.18.0) has one new `LOSSY` line: `CreateElicitationRequest` loses `example` in its `other` mode, whose schema says `"unevaluatedProperties": true`. With `CreateElicitationOther.AdditionalProperties` the line is gone.

The Rust probe (2026-11-19, schema 1.32.0, `main` at `6217b27`, v0.20.0): the pin check passes, because crate 1.18.0 and `schema-v1.32.0` are both upstream commit `13b7658`. The 21 committed cases give 8 `same`, 1 `both-reject`, 3 `differs`, 7 `go-only` and 2 `rust-only`: every known difference of #82 and #125 shows, and every control and envelope case agrees. The 1669 bad values of a `G2_PROBES` file all read the same. On `main` at `093e73c`, after #130, #131 and #132, the cases give 13 `same`, 1 `both-reject` and 7 `go-only`. With #133, #134 and #138 on top, they give 13 `same` and 8 `both-reject`, with 0 `differs`, `go-only`, `rust-only` or `no-type`, and `rust-diff` prints `RUST PASS`.

Before those four PRs, G2 reported 183 distinct problems on `main` at `a0c38c3`: `HINT` 165 (the SDK honored no hint, and the test did not check a property in its union yet), `LOSSY` 14 (the 3 lines above, `_meta` and `annotations` of a `ContentBlock`, and properties of the unstable elicitation types) and `SCHEMA` 4 (encoded elicitation types without required properties).

G7 (`g7-canary`): passes. `docker/docker-agent` at `19a7356` (1 package), `jaegertracing/jaeger` at `f141f57` (3 packages) and `coder/coder` at `a3f25a4` (2 packages: `agent/x/agentacp`, `coderd/x/chatd`) build against `main`. A checkout with `NewInvalidParams` and `PromptResponse.StopReason` renamed fails docker-agent with two groups, `acp.NewInvalidParams (29)` and `acp.PromptResponse.StopReason (5)`.
