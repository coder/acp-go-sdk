module verifyschemabump

go 1.26.0

require (
	github.com/coder/acp-go-sdk v0.0.0
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3
)

require (
	golang.org/x/exp v0.0.0-20261113113407-e3970619a1a4 // indirect
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/text v0.14.0 // indirect
	golang.org/x/tools v0.52.1-0.20261106145633-d407941a339a // indirect
)

replace github.com/coder/acp-go-sdk => ../../..

tool golang.org/x/exp/cmd/apidiff
