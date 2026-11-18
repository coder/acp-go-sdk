# Repository Guidelines

## Project Structure & Module Organization

Core SDK code lives at the repo root (`agent.go`, `client.go`, `connection.go`, `errors.go`). Generated unions and helpers carry the `_gen.go` suffix; refresh them through `cmd/generate`. Protocol schemas and version pins reside in `schema/`, while fixtures live under `testdata/`. Example agents and clients that mirror real integrations sit in `example/`, and double as integration smoke tests.

## Build, Test, and Development Commands

- `go test ./...` exercises unit tests across packages.
- `go run ./example/agent` and `go run ./example/client` provide quick manual validation of agent/client behavior.
- `make test` runs `go vet` and `go test` for the SDK and `cmd/generate`, builds the examples, and runs the verification skills' offline unit tests: `acpwire` (verify-acp-go-sdk), `acpharness` (verify-real-harnesses), and `methodcheck` and the roundtrip helpers of verify-schema-bump. CI runs `make check` and `make test`. The gates themselves run by hand from their skills, because they need the network, a model, npm or Java: G1, G2's data checks and G7 (verify-schema-bump), G3 (verify-acp-go-sdk), G4 and G5 (verify-real-harnesses), G6 (verify-notification-barrier, verify-session-cancel).
- `make fmt` runs `treefmt` so Go and Markdown stay formatted consistently.
- `make check` runs `treefmt --fail-on-change` plus the README guard used in CI.
- `mise install` provisions the toolchain (Go, gopls, golangci-lint, treefmt, etc.); run it once after cloning.

## Coding Style & Naming Conventions

Target Go 1.21 idioms: tabs for indentation, short receiver names, and CamelCase for exported symbols (`AgentLoader`), lowerCamelCase for unexported helpers. Prefer table-driven tests and keep files focused on a single protocol concern. Run `make fmt` or `gofumpt -w .` to normalize spacing, imports, and composite literals before sending a change.

## Testing Guidelines

New behavior needs corresponding `*_test.go` coverage. Name tests `TestType_Action` to group assertions, and place fixtures under `testdata/` when JSON payloads are required. Use `go test ./... -run TestName` for fast iteration and `go test ./... -cover` to confirm coverage does not regress. Keep example binaries compiling; they are part of the `make test` target.

## Commit & Pull Request Guidelines

Write commit subjects in the imperative mood (`Add request codec`, `Fix session timeout`) and keep the first line under ~72 characters. Reference issues with `Fixes #123` when applicable. Pull requests should summarize protocol impact, list any schema or generated file updates, and note the tests or commands you ran (`make test`, `go run ./example/client`). Include screenshots or terminal transcripts only when they clarify agent/client interactions.

## Code Generation & Schema Updates

Schema bumps originate from `schema/version`. After editing it, run `make generate` to re-fetch upstream JSON and regenerate bindings. Commit the updated schemas, generated Go, and `schema/version` together so reviewers can track protocol changes. The top-level `version` file is the SDK's own semver: bump it only in a release PR (see `RELEASING.md`).
