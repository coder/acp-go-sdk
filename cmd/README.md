# cmd

## Overview

`cmd` holds the command-line programs of the repository. It has one program, `generate`, the code generator that writes the `*_gen.go` files of the root `acp` package from the JSON schemas in `schema/`. `generate` is a separate Go module, `github.com/coder/acp-go-sdk/cmd/generate`, so `go test ./...` at the repository root does not run its tests.

## Subdirectories & Boundaries

- generate: [./generate/README.md](./generate/README.md) - Reads `schema/meta.json`, `schema/schema.json`, and the optional unstable files, merges them, and writes `constants_gen.go`, `types_gen.go`, `agent_gen.go`, `client_gen.go`, and `helpers_gen.go`. By default it reads `<repo>/schema` and writes to the repository root. `-schema` sets the input directory and `-out` sets the output directory. The generator still needs a repository root above the working directory (the nearest ancestor with `schema/meta.json` and `schema/schema.json`), and it panics when none exists. The root `Makefile` target `version` runs it with `cd cmd/generate && env -u GOPATH -u GOMODCACHE go run .`.
