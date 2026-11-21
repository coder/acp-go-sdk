# generate

## Overview

`cmd/generate` is the code generator for the ACP Go SDK. It is a separate Go module, `github.com/coder/acp-go-sdk/cmd/generate`, with Go 1.26 and two dependencies: `github.com/dave/jennifer v1.7.1`, and `golang.org/x/exp` for `apidiff` in `internal/stablecheck`.
`main.go` reads the ACP meta and schema inputs, merges the unstable inputs when they exist, checks that the merge keeps every stable Go type, and writes the files with `emit.WriteFiles`.
The rules that map the schema to Go types are in the READMEs of [`internal/ir`](./internal/ir/README.md) and [`internal/emit`](./internal/emit/README.md), such as which properties are patch fields (`acp.Patch[T]`) and which values stay raw JSON.
The root `Makefile` target `generate` runs it with `cd cmd/generate && env -u GOPATH -u GOMODCACHE go run .`, so the output goes to the repository root by default.
The target first downloads the schema files of the release in `schema/version` when they are older than that file. It never writes the top-level `version` file, which holds the SDK's own semver [Makefile:generate].

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
- The merge fails, and so the generator panics, when the unstable version of a definition that both schemas share removes a property, requires a member, or changes a type. The panic lists each change by path, such as `SetSessionConfigOptionRequest.value: removed` at schemas 0.13.5 to 1.17.0 ([internal/load](./internal/load/README.md)) [merge.go:promoteChangedSharedDefs]
- With the unstable inputs, `stablecheck.Compare` writes the files of the stable inputs alone and of the merged ones into a temporary folder and compares the stable types they declare: their API, and the code of their methods beyond what the merge adds. A change panics with a line for each, such as `EmbeddedResourceResource.TextResourceContents: removed`, before any file reaches the output directory ([internal/stablecheck](./internal/stablecheck/README.md)) [main.go:main]
- `emit.WriteFiles` writes `constants_gen.go`, `types_gen.go`, `agent_gen.go`, `client_gen.go` and, with proxy methods, `proxy_gen.go`, then `helpers_gen.go`, which refers to the generated structs [main.go:main] [files.go:WriteFiles]
- A bad command-line flag makes `flag.Parse` print the error and exit with status 2 (`flag.ExitOnError`). After parsing, every read, merge, or emit error panics [main.go:main]

## Side Effects

- Parses command-line flags with `flag.Parse` [main.go:main]
- Reads the working directory with `os.Getwd`. In each ancestor it calls `os.Stat` on `schema/meta.json`, and on `schema/schema.json` only when `schema/meta.json` exists [main.go:findRepoRoot, hasSchema, fileExists]
- Creates the output directory and any missing parents with `os.MkdirAll` and mode `0o755` (before umask). An existing directory keeps its permissions [main.go:main]
- Reads the schema version, meta, schema, and unstable files through `load.ReadSchemaVersion`, `load.ReadMeta`, `load.ReadSchema`, `load.ReadMetaUnstable`, and `load.ReadSchemaUnstable`. The schema version becomes the `SchemaVersion` constant [main.go:main]
- Overwrites `constants_gen.go`, `types_gen.go`, `agent_gen.go`, `client_gen.go`, `proxy_gen.go`, and `helpers_gen.go` in the output directory through `emit.WriteFiles` [main.go:main]
- Ends the process with exit status 2 on a flag parse error, and with a panic on any later error [main.go:main]

## Verification

- Run: `cd cmd/generate && go test ./...`
