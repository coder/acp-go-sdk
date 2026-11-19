package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/coder/acp-go-sdk/cmd/generate/internal/emit"
	"github.com/coder/acp-go-sdk/cmd/generate/internal/load"
	"github.com/coder/acp-go-sdk/cmd/generate/internal/stablecheck"
)

func main() {
	var schemaDirFlag string
	var outDirFlag string
	flag.StringVar(&schemaDirFlag, "schema", "", "path to schema directory (defaults to <repo>/schema)")
	flag.StringVar(&outDirFlag, "out", "", "output directory for generated go files (defaults to <repo>)")
	flag.Parse()

	repoRoot := findRepoRoot()
	schemaDir := schemaDirFlag
	outDir := outDirFlag
	if schemaDir == "" {
		schemaDir = filepath.Join(repoRoot, "schema")
	}
	if outDir == "" {
		outDir = repoRoot
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		panic(err)
	}

	schemaVersion, err := load.ReadSchemaVersion(schemaDir)
	if err != nil {
		panic(err)
	}

	meta, err := load.ReadMeta(schemaDir)
	if err != nil {
		panic(err)
	}

	schema, err := load.ReadSchema(schemaDir)
	if err != nil {
		panic(err)
	}

	unstableMeta, unstableMetaFound, err := load.ReadMetaUnstable(schemaDir)
	if err != nil {
		panic(err)
	}
	unstableSchema, unstableSchemaFound, err := load.ReadSchemaUnstable(schemaDir)
	if err != nil {
		panic(err)
	}
	if unstableMetaFound != unstableSchemaFound {
		panic(fmt.Sprintf("unstable schema/meta mismatch: meta found=%v schema found=%v", unstableMetaFound, unstableSchemaFound))
	}
	if unstableMetaFound {
		mergedMeta, mergedSchema, err := load.MergeStableAndUnstable(meta, schema, unstableMeta, unstableSchema)
		if err != nil {
			panic(err)
		}
		// The merge must leave every stable Go type as the stable schema
		// alone writes it, but for additions.
		if err := stablecheck.Compare(repoRoot, schemaVersion, stablecheck.Run{Meta: meta, Schema: schema}, stablecheck.Run{Meta: mergedMeta, Schema: mergedSchema}); err != nil {
			panic(err)
		}
		meta = mergedMeta
		schema = mergedSchema
	}

	if err := emit.WriteFiles(outDir, schema, meta, schemaVersion); err != nil {
		panic(err)
	}
}

func findRepoRoot() string {
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	dir := cwd
	for {
		if hasSchema(dir) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			panic(fmt.Sprintf("could not locate repository root from %q; ensure schema files exist", cwd))
		}
		dir = parent
	}
}

func hasSchema(dir string) bool {
	if dir == "" {
		return false
	}
	metaPath := filepath.Join(dir, "schema", "meta.json")
	schemaPath := filepath.Join(dir, "schema", "schema.json")
	return fileExists(metaPath) && fileExists(schemaPath)
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	if _, err := os.Stat(path); err == nil {
		return true
	}
	return false
}
