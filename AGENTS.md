# Repository Guidelines

## Project Structure & Module Organization

Core SDK code lives at the repo root (`agent.go`, `client.go`, `connection.go`, `errors.go`). Generated unions and helpers carry the `_gen.go` suffix; refresh them through `cmd/generate`. Protocol schemas and version pins reside in `schema/`, while fixtures live under `testdata/`. Example agents and clients that mirror real integrations sit in `example/`, and double as integration smoke tests.

## Build, Test, and Development Commands

- `go test ./...` exercises unit tests across packages.
- `go run ./example/agent` and `go run ./example/client` provide quick manual validation of agent/client behavior.
- `make test` runs `go vet` and `go test` for the SDK and `cmd/generate`, builds the examples, and runs the verification skills' offline unit tests: `acpwire` (verify-acp-go-sdk), `acpharness` (verify-real-harnesses), `methodcheck` and the roundtrip helpers of verify-schema-bump, and the conformance agent and `tckcheck` of verify-tck. CI runs `make check`, `make test`, `make test-module-zip`, `make test-386` and `make tck`. The gates themselves run by hand from their skills, because they need the network, a model, npm, cargo or Java: G1, G2's data checks, the Rust probe and G7 (verify-schema-bump), G3 (verify-acp-go-sdk), G4 and G5 (verify-real-harnesses), G6 (verify-notification-barrier, verify-session-cancel).
- `make test-module-zip` runs `go vet` and `go test` in the module zip that the Go proxy would serve for the checkout (`scripts/test-module-zip`). That zip leaves out every folder with a go.mod of its own (`testdata/bench`, `cmd/generate`, `scripts/modzip`, the verify skills that are modules), so a test reading from one of them must go through `repoFiles` in `repo_files_test.go`. CI runs it after `make test`.
- `make test-386` runs `go vet` and `go test` for the SDK and `cmd/generate` with `GOARCH=386`, where `int` is 32 bits, so a wire value that fits `int` on amd64 and wraps on 32-bit fails. CI runs it.
- `make tck` runs the ACP Test Compatibility Kit, pinned in verify-tck's `runner/`, against the conformance agent and fails when a result drifts from `baseline.json`; after a change that moves a result, `make tck-update` rewrites the baseline and you write a reason for each requirement that does not pass. It needs uv, cgo with a C compiler (the agent is built with `-race`) and, on the first run, the network.
- `make fmt` runs `treefmt` so Go and Markdown stay formatted consistently.
- `make check` runs `scripts/check-code-citations` (no line numbers in the model-to-code tables of verify-notification-barrier and verify-session-cancel), `scripts/check-model-fingerprints` (the code those TLA+ models cover still matches their `fingerprint.txt`), then `treefmt`, and fails when formatting leaves a diff.
- `mise install` provisions the toolchain (Go, gopls, golangci-lint, treefmt, etc.); run it once after cloning.

## Coding Style & Naming Conventions

Target Go 1.21 idioms: tabs for indentation, short receiver names, and CamelCase for exported symbols (`AgentLoader`), lowerCamelCase for unexported helpers. Prefer table-driven tests and keep files focused on a single protocol concern. Run `make fmt` before sending a change: it runs `gofmt` (through `treefmt`), which `make check` enforces. Do not run `gofumpt -w .`: it rewrites files outside your change that the repo formats with `gofmt`, generated ones included.

## Testing Guidelines

New behavior needs corresponding `*_test.go` coverage. Name tests `TestType_Action` to group assertions, and place fixtures under `testdata/` when JSON payloads are required. Use `go test ./... -run TestName` for fast iteration and `go test ./... -cover` to confirm coverage does not regress. Keep example binaries compiling; they are part of the `make test` target.

## Commit & Pull Request Guidelines

Write commit subjects in the imperative mood (`Add request codec`, `Fix session timeout`) and keep the first line under ~72 characters. Reference issues with `Fixes #123` when applicable. Pull requests should summarize protocol impact, list any schema or generated file updates, and note the tests or commands you ran (`make test`, `go run ./example/client`). Include screenshots or terminal transcripts only when they clarify agent/client interactions.

## Code Generation & Schema Updates

Schema bumps originate from `schema/version`. After editing it, run `make generate` to re-fetch upstream JSON and regenerate bindings. Commit the updated schemas, generated Go, and `schema/version` together so reviewers can track protocol changes. The top-level `version` file is the SDK's own semver: bump it only in a release PR (see `RELEASING.md`).

## Rules and what enforces them

Each rule came from a mistake that reached review at least twice. When a rule has a check, the check is the rule: fix what it reports. When nothing enforces a rule and the mistake happens again, add a check (or change the code so the mistake cannot be written) in the same change, and update this table. Drop a row once its mistake cannot happen.

| Rule | Enforced by |
| --- | --- |
| A test reads a file from a folder with its own `go.mod` (`testdata/bench`, `cmd/generate`, the verify skills that are modules) only through `repoFiles` (`repo_files_test.go`). | `make test-module-zip` in CI |
| A model-to-code table in a skill with a `fingerprint.txt` names the function and quotes the code, never a line number. | `scripts/check-code-citations` in `make check` |
| A change to code that a TLA+ model covers reruns that skill's `check.sh` and commits its `fingerprint.txt` in the same change. | `scripts/check-model-fingerprints` in `make check` (TLC itself runs by hand) |
| A value from the wire or the schema that can exceed 2^31 is not held in `int`. | `make test-386` in CI, for values that a test reaches |
| A doc does not state a count of something on `main` that the next PR can change (types, methods, cases); count only what a tag fixes, or give the command that counts. | Nothing: judgment |
| Every sentence a change adds or keeps in a migration guide, README or doc comment is true for the code at the head, including each "before" clause against the old tag; every Go snippet compiles. | Nothing: a verifier reads each sentence |
| A new assertion fails on the defect it guards: break the code, watch the test fail, restore it. | Nothing: judgment |
| A fix covers every entry point and schema form that reaches the broken code, not only the one in the report. | Nothing: judgment |
