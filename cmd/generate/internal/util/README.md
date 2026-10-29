# util

## Overview

Package `util` holds pure string and identifier helpers for the code generator.
`github.com/coder/acp-go-sdk/cmd/generate/internal/emit` and `github.com/coder/acp-go-sdk/cmd/generate/internal/ir` use them to build Go identifiers and doc comments from schema names and descriptions.
The package imports only `strings` and `unicode`.

## Interface

- `func SanitizeComment(s string) string` replaces backticks with single quotes, trims each line, and joins the lines with spaces. Its comment marks it `Deprecated` in favor of `FormatDocComment` [util.go:SanitizeComment]
- `func FormatDocComment(desc string) []string` replaces backticks with single quotes and returns one trimmed entry per line. Blank lines stay as empty strings [util.go:FormatDocComment]
- `func TitleWord(s string) string` uppercases the first rune and lowercases the rest [util.go:TitleWord]
- `func SplitCamel(s string) []string` splits `s` before each lowercase-to-uppercase ASCII boundary and before each `_` after the first byte [util.go:SplitCamel, isBoundary]
- `func ToExportedField(name string) string` converts a schema identifier into an exported PascalCase Go identifier [util.go:ToExportedField]
- `func ToEnumConst(typeName, val string) string` returns `typeName` followed by the title-cased words of `val` [util.go:ToEnumConst]

## Invariants

- `FormatDocComment` returns nil for an empty description [util.go:FormatDocComment]
- `TitleWord` returns an empty input unchanged [util.go:TitleWord]
- `SplitCamel` always returns at least one part, also for an empty string [util.go:SplitCamel]
- `ToExportedField` treats every rune other than ASCII letters and digits as a word separator, splits each word with `SplitCamel`, drops empty tokens, and applies `TitleWord` to each token [util.go:ToExportedField]
- `ToEnumConst` treats every rune other than ASCII letters and digits as a separator, drops empty parts, and lowercases each part before `TitleWord` [util.go:ToEnumConst]

## Side Effects

- None

## Verification

- Run: `cd cmd/generate && go test ./internal/util/...`
