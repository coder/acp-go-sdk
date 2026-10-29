# internal

## Overview

`internal` holds the four packages of the ACP Go SDK code generator, which only code under `github.com/coder/acp-go-sdk/cmd/generate` can import.
Data flows in one direction: `load` reads the schema inputs, `ir` groups methods and bindings, and `emit` writes the generated Go files.
`util` supplies the naming and comment helpers that `ir` and `emit` share.

## Subdirectories & Boundaries

- emit: [./emit/README.md](./emit/README.md) - Renders `constants_gen.go`, `types_gen.go`, `agent_gen.go`, `client_gen.go`, and `helpers_gen.go` for the root `acp` package with jennifer.
- ir: [./ir/README.md](./ir/README.md) - Maps wire methods to Go Request, Response, and Notification types and to interface bindings. No I/O.
- load: [./load/README.md](./load/README.md) - Reads `meta.json`, `schema.json`, and the optional unstable files, and merges stable and unstable inputs.
- util: [./util/README.md](./util/README.md) - Pure string and identifier helpers for Go names and doc comments.
