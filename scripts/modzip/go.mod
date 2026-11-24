// scripts/modzip is a module of its own so that the SDK module keeps no
// dependencies. The module zip leaves this folder out.
module github.com/coder/acp-go-sdk/scripts/modzip

go 1.26.0

require golang.org/x/mod v0.42.0
