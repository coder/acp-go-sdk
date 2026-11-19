# stablecheck

## Overview

Package `stablecheck` fails a merge of the unstable schema that changes a stable Go type.
It does not predict what the emitter makes of a schema: it runs the emitter twice, on the stable schema alone and on the merged one, and compares what the first output declares with the second.
A type's code can depend on other definitions, such as the const properties of a definition that a union variant refers to, so a comparison of schema definitions cannot see every change; the generated code shows each one.

## Interface

- `type Run struct { Meta *load.Meta; Schema *load.Schema }`: the input of one generator run [stablecheck.go:Run]
- `func Compare(pkgDir, schemaVersion string, stable, merged Run) error` returns nil, or an error with one line per change to a stable type [stablecheck.go:Compare]

## Invariants

- `Compare` writes the files of each run with `emit.WriteFiles` and type-checks them with the hand-written non-test files of `pkgDir`. The standard library is imported from source. Type errors do not stop it, since the hand-written code may refer to what only the merged files declare [stablecheck.go:Compare, typecheck]
- The stable types are the package-level names that the stable run's generated files declare. Each incompatible change that `apidiff` reports for one of them is a change, such as `EmbeddedResourceResource.TextResourceContents: removed` or `ContentBlock.Text: changed from *ContentBlockText to *ContentBlockTextInline`. Added fields, variants, methods, and types are compatible [stablecheck.go:Compare, subject] [stablecheck_test.go:TestCompare_FailsOnChangesToStableTypes]
- The body of each function and method of the stable run's generated files, except methods of hand-written types such as `AgentSideConnection.handle`, must equal its merged version once the merged version loses each statement, switch case, and local struct field that refers to an added name (a field that the merged receiver type adds, or a package-level name that only the merged run declares) and to no stable field of the receiver type. A statement that refers to both is compared element by element. Otherwise the error quotes the first line that differs, such as `Caps.UnmarshalJSON: changes; stable has "..." where merged has "..."` [stablecheck.go:bodyChanges, projection, filter] [stablecheck_test.go:TestCompare_AllowsAdditions]
- In `UnmarshalJSON`, a removed statement that can return with a `nil` error before the code refers to a stable field again is a change: the new variant would take values that a stable one decoded, as a titled `number` variant before a titled `integer` one does. A case of a switch whose stable cases stay may go anywhere, since the cases compare distinct constants, such as the values of a discriminator [stablecheck.go:earlyReturns, returnsNil] [stablecheck_test.go:TestCompare_FailsOnChangesToStableTypes]
- A string literal that holds a JSON object or array and is passed to a call with a pointer, such as the default of `setDefault`, is compared in one JSON form. Its merged version first loses the members for the fields that the merged version of the pointed-to type adds, at any depth, so a default may gain members for added properties [stablecheck.go:canonicalJSON, prune]
- Lines are compared without indentation and without empty lines [stablecheck.go:text]

## Side Effects

- Creates a temporary folder with `os.MkdirTemp`, writes both runs' generated files into it, and removes it before returning [stablecheck.go:Compare]
- Reads the `*.go` files of `pkgDir` and the Go standard library source [stablecheck.go:typecheck]
- The syntax trees that it edits are its own parse of the files it wrote [stablecheck.go:bodyChanges, canonicalJSON]

## Verification

- Run: `cd cmd/generate && go test ./internal/stablecheck/`
