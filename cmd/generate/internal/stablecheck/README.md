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
- The stable types are the package-level names that the stable run's generated files declare. Each change that `apidiff` reports for one of them is a change unless it is an addition (`: added`), such as `EmbeddedResourceResource.TextResourceContents: removed`, `ContentBlock.Text: changed from *ContentBlockText to *ContentBlockTextInline`, or `ProtocolVersion: changed from int to int64`, which `apidiff` calls compatible. Added fields, variants, methods, and types pass, with one exception: a field whose Go type cannot be compared with `==`, such as a map `_meta`, added to a stable struct that could be compared before, fails as `<T>: old is comparable, new is not`, because `==` on that type stops compiling. Six stable structs can hit this at schema 1.32.0, among them `NoticeCapabilities` and `SessionConfigBoolean` [stablecheck.go:Compare, subject] [stablecheck_test.go:TestCompare_FailsOnChangesToStableTypes]
- The body of each function and method of the stable run's generated files, except methods of hand-written types such as `AgentSideConnection.handle`, must equal its merged version once the merged version loses each statement, switch case, case value, local struct field, and composite literal element that refers to an added name (a field that the merged receiver type adds, or a package-level name that only the merged run declares) and to no stable name. The stable names are the fields of a struct receiver, else the package-level names of the stable run's generated files, such as the constants of an enum. An identifier counts by the `go/types` object it denotes: a field only as a field name, a package-level object only as a package-level name, and a local, such as the type `Alias` of a `MarshalJSON`, as neither, so an added property `alias` or `update` of `Update` passes. A statement that refers to both is compared element by element. Otherwise the error quotes the first line that differs, such as `Caps.UnmarshalJSON: changes; stable has "..." where merged has "..."` [stablecheck.go:bodyChanges, projection, filter] [stablecheck_test.go:TestCompare_AllowsAdditions]
- In `UnmarshalJSON`, a removed statement that can return with a `nil` error before the code refers to a stable field again is a change: the new variant would take values that a stable one decoded, as a titled `number` variant before a titled `integer` one does. A return in a literal case of a switch with a tag is exempt, since the case takes only its own values: a new variant may get a case of the switch on a discriminator anywhere, and a new switch on a new discriminator may go before the stable variants [stablecheck.go:earlyReturns, returnsNil, literalCase] [stablecheck_test.go:TestCompare_FailsOnChangesToStableTypes]
- A method that the merged run adds to a stable type, such as an `UnmarshalJSON` that a hint brings, may refer only to added names once projected, else it is a change such as `Plain.UnmarshalJSON: is new and refers to the stable Mode` [stablecheck.go:bodyChanges, newProjection] [stablecheck_test.go:TestCompare_FailsOnChangesToStableTypes]
- A field of a stable struct type keeps its tag, which `apidiff` does not read: `TerminalOutputResponse.Truncated: changes its tag from ... to ...,omitempty` when a member is no longer required [stablecheck.go:tagChanges]
- Each entry of the `shapes` table, which the decoder checks hinted values against (`shape.go`), keeps every rule but for additions: values (an enum or a const), members, and alternatives. A `not` rule compares the other way, since a value fits only if it does not fit the `not` rule: the merged one may take only what the stable one takes, else `shapes[shapeHintedLabel].not: takes values that the stable rule does not`. The one exception is an alternative of a union that gains alternatives, whose `not` rule may gain alternatives, as a catch-all variant does when its union gains a variant. Kinds, required members, `ref`, `rest`, and `items` must stay, and a rule that takes any value must not gain values, as `shapes[shapeHintedLabel]: takes only the values ["x"]` says. An entry of a form the check does not model fails it, such as `shapes[shapeTextResourceContentsMimeType]: loses the value "text/markdown"` [shapes.go:shapeChanges, rule.lostIn, parseRule]
- An added `if` statement may take the place of a stable one with its condition that only returns an error: the merged code then accepts values that the stable code rejected, as when a union gains a null alternative [stablecheck.go:projection.acceptRejected]
- A string literal that holds a JSON object or array and is passed to a call with a pointer, such as the default of `setDefault`, is compared in one JSON form. Its merged version first loses the members for the fields that the merged version of the pointed-to type adds, at any depth, so a default may gain members for added properties [stablecheck.go:canonicalJSON, prune]
- Lines are compared without indentation and without empty lines, token by token: the spaces between two tokens count as one, and a string literal keeps its own, so a default `"a  b"` that becomes `"a b"` is a change [stablecheck.go:text]

## Side Effects

- Creates a temporary folder with `os.MkdirTemp`, writes both runs' generated files into it, and removes it before returning [stablecheck.go:Compare]
- Reads the `*.go` files of `pkgDir` and the Go standard library source [stablecheck.go:typecheck]
- The syntax trees that it edits are its own parse of the files it wrote [stablecheck.go:bodyChanges, canonicalJSON]

## Verification

- Run: `cd cmd/generate && go test ./internal/stablecheck/`
