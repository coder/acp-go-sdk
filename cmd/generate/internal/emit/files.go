package emit

import "github.com/coder/acp-go-sdk/cmd/generate/internal/load"

// WriteFiles writes every generated file of the acp package into outDir:
// constants_gen.go, types_gen.go, agent_gen.go and client_gen.go, then
// helpers_gen.go, which refers to the generated structs.
func WriteFiles(outDir string, schema *load.Schema, meta *load.Meta, schemaVersion string) error {
	if err := WriteConstantsJen(outDir, meta, schemaVersion); err != nil {
		return err
	}
	if err := WriteTypesJen(outDir, schema, meta); err != nil {
		return err
	}
	if err := WriteDispatchJen(outDir, schema, meta); err != nil {
		return err
	}
	return WriteHelpersJen(outDir, schema, meta)
}
