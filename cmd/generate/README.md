# generate

## Overview

`cmd/generate` is the code generator for the ACP Go SDK. It is a separate Go module, `github.com/coder/acp-go-sdk/cmd/generate`, with Go 1.21 and one dependency, `github.com/dave/jennifer v1.7.1`.
`main.go` reads the ACP meta and schema inputs, merges the unstable inputs when they exist, and calls the four `emit` writers in a fixed order.
The root `Makefile` target `version` runs it with `cd cmd/generate && env -u GOPATH -u GOMODCACHE go run .`, so the output goes to the repository root by default.
After the generator, that target runs `gofumpt -w .` over the whole repository and writes the `version` stamp file [Makefile:version].

## Interface

- CLI flag `-schema` (string, default `""`): path to the schema directory. Empty means `<repo>/schema` [main.go:main]
- CLI flag `-out` (string, default `""`): output directory for the generated Go files. Empty means the repository root [main.go:main]
- Entry point `func main()`. The package has no exported symbols [main.go:main]
- Unexported helpers `findRepoRoot`, `hasSchema`, and `fileExists` locate the repository root [main.go:findRepoRoot, hasSchema, fileExists]
- Generator packages: [./internal/README.md](./internal/README.md)

## Invariants

- The repository root is the nearest ancestor of the working directory that contains both `schema/meta.json` and `schema/schema.json`. If none exists, the generator panics [main.go:findRepoRoot, hasSchema]
- `findRepoRoot` runs even when both flags are set [main.go:main]
- `meta.unstable.json` and `schema.unstable.json` must both exist or both be absent. A mismatch panics [main.go:main]
- When the unstable inputs exist, `load.MergeStableAndUnstable` replaces the stable meta and schema with the merged result before any emit step [main.go:main]
- The emit order is `WriteConstantsJen`, `WriteTypesJen`, `WriteDispatchJen`, then `WriteHelpersJen`. The code comment says helpers come after types so they can reference generated structs [main.go:main]
- A bad command-line flag makes `flag.Parse` print the error and exit with status 2 (`flag.ExitOnError`). After parsing, every read, merge, or emit error panics [main.go:main]

## Side Effects

- Parses command-line flags with `flag.Parse` [main.go:main]
- Reads the working directory with `os.Getwd`. In each ancestor it calls `os.Stat` on `schema/meta.json`, and on `schema/schema.json` only when `schema/meta.json` exists [main.go:findRepoRoot, hasSchema, fileExists]
- Creates the output directory and any missing parents with `os.MkdirAll` and mode `0o755` (before umask). An existing directory keeps its permissions [main.go:main]
- Reads the meta, schema, and unstable files through `load.ReadMeta`, `load.ReadSchema`, `load.ReadMetaUnstable`, and `load.ReadSchemaUnstable` [main.go:main]
- Overwrites `constants_gen.go`, `types_gen.go`, `agent_gen.go`, `client_gen.go`, and `helpers_gen.go` in the output directory through the `emit` writers [main.go:main]
- Ends the process with exit status 2 on a flag parse error, and with a panic on any later error [main.go:main]

## Verification

- Run: `cd cmd/generate && go test ./...`
