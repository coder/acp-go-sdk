# Repository Guidelines

## Project Structure & Module Organization

Core SDK code lives at the repo root (`agent.go`, `client.go`, `connection.go`, `errors.go`). Generated unions and helpers carry the `_gen.go` suffix; refresh them through `cmd/generate`. Protocol schemas and version pins reside in `schema/`, while fixtures live under `testdata/`. Example agents and clients that mirror real integrations sit in `example/`, and double as integration smoke tests.

## Build, Test, and Development Commands

- `go test ./...` exercises unit tests across packages.
- `go run ./example/agent` and `go run ./example/client` provide quick manual validation of agent/client behavior.
- `make test` runs `go vet` and `go test` for the SDK and `cmd/generate`, builds the examples, and runs the verification skills' offline unit tests: `acpwire` (verify-acp-go-sdk), `acpharness` (verify-real-harnesses), `methodcheck` and the roundtrip helpers of verify-schema-bump, and the conformance agent and `tckcheck` of verify-tck. CI runs `make check`, `make test`, `make test-module-zip` and `make tck`. The gates themselves run by hand from their skills, because they need the network, a model, npm, cargo or Java: G1, G2's data checks, the Rust probe and G7 (verify-schema-bump), G3 (verify-acp-go-sdk), G4 and G5 (verify-real-harnesses), G6 (verify-notification-barrier, verify-session-cancel).
- `make test-module-zip` runs `go vet` and `go test` in the module zip that the Go proxy would serve for the checkout (`scripts/test-module-zip`). That zip leaves out every folder with a go.mod of its own (`testdata/bench`, `cmd/generate`, `scripts/modzip`, the verify skills that are modules), so a test reading from one of them must go through `repoFiles` in `repo_files_test.go`. CI runs it after `make test`.
- `make tck` runs the ACP Test Compatibility Kit, pinned in verify-tck's `runner/`, against the conformance agent and fails when a result drifts from `baseline.json`; after a change that moves a result, `make tck-update` rewrites the baseline and you write a reason for each requirement that does not pass. It needs uv, cgo with a C compiler (the agent is built with `-race`) and, on the first run, the network.
- `make fmt` runs `treefmt` so Go and Markdown stay formatted consistently.
- `make check` runs `scripts/check-code-citations` (no line numbers in the model-to-code tables of verify-notification-barrier and verify-session-cancel), then `treefmt`, and fails when formatting leaves a diff.
- `mise install` provisions the toolchain (Go, gopls, golangci-lint, treefmt, etc.); run it once after cloning.

## Coding Style & Naming Conventions

Target Go 1.21 idioms: tabs for indentation, short receiver names, and CamelCase for exported symbols (`AgentLoader`), lowerCamelCase for unexported helpers. Prefer table-driven tests and keep files focused on a single protocol concern. Run `make fmt` or `gofumpt -w .` to normalize spacing, imports, and composite literals before sending a change.

## Testing Guidelines

New behavior needs corresponding `*_test.go` coverage. Name tests `TestType_Action` to group assertions, and place fixtures under `testdata/` when JSON payloads are required. Use `go test ./... -run TestName` for fast iteration and `go test ./... -cover` to confirm coverage does not regress. Keep example binaries compiling; they are part of the `make test` target.

## Commit & Pull Request Guidelines

Write commit subjects in the imperative mood (`Add request codec`, `Fix session timeout`) and keep the first line under ~72 characters. Reference issues with `Fixes #123` when applicable. Pull requests should summarize protocol impact, list any schema or generated file updates, and note the tests or commands you ran (`make test`, `go run ./example/client`). Include screenshots or terminal transcripts only when they clarify agent/client interactions.

## Code Generation & Schema Updates

Schema bumps originate from `schema/version`. After editing it, run `make generate` to re-fetch upstream JSON and regenerate bindings. Commit the updated schemas, generated Go, and `schema/version` together so reviewers can track protocol changes. The top-level `version` file is the SDK's own semver: bump it only in a release PR (see `RELEASING.md`).
