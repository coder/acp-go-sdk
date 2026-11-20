package stablecheck

import (
	"go/importer"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestEmptyAdded checks what the MarshalJSON of T, whose field Extra only
// the merged type has, returns for a stable value once emptyAdded and
// foldMarshal have run: the stable return json.Marshal(a) only when the
// helper that Extra goes to returns the bytes at once for an empty Extra.
func TestEmptyAdded(t *testing.T) {
	const open = `named, err := json.Marshal(a)
		if err != nil {
			return nil, err
		}
		return withExtra(named, v.Extra)`
	cases := []struct {
		name, guard, body string
		want              []string
	}{
		{
			name:  "the helper returns the bytes for an empty map",
			guard: "if len(extra) == 0 {\n\t\treturn named, nil\n\t}",
			body:  open,
			want:  []string{"return json.Marshal(a)"},
		},
		{
			name:  "the helper changes the bytes for an empty map",
			guard: "if len(extra) == 0 {\n\t\treturn []byte(\"{}\"), nil\n\t}",
			body:  open,
			want:  []string{"named, err := json.Marshal(a)", "if err != nil {", "return nil, err", "}", "return withExtra(named, v.Extra)"},
		},
		{
			name:  "the helper returns the bytes only for a map that is not empty",
			guard: "if len(extra) > 0 {\n\t\treturn named, nil\n\t}",
			body:  open,
			want:  []string{"named, err := json.Marshal(a)", "if err != nil {", "return nil, err", "}", "return withExtra(named, v.Extra)"},
		},
		{
			name:  "the helper returns the bytes for a map of one member",
			guard: "if len(extra) == 1 {\n\t\treturn named, nil\n\t}",
			body:  open,
			want:  []string{"named, err := json.Marshal(a)", "if err != nil {", "return nil, err", "}", "return withExtra(named, v.Extra)"},
		},
		{
			// encode returns bytes beside an error.
			name:  "the bytes come from another call",
			guard: "if len(extra) == 0 {\n\t\treturn named, nil\n\t}",
			body:  "named, err := encode(a)\n\t\tif err != nil {\n\t\t\treturn nil, err\n\t\t}\n\t\treturn withExtra(named, v.Extra)",
			want:  []string{"named, err := encode(a)", "if err != nil {", "return nil, err", "}", "return named, nil"},
		},
		{
			name:  "a stable field goes to the helper",
			guard: "if len(extra) == 0 {\n\t\treturn named, nil\n\t}",
			body:  "named, err := json.Marshal(a)\n\t\tif err != nil {\n\t\t\treturn nil, err\n\t\t}\n\t\treturn withExtra(named, v.Tags)",
			want:  []string{"named, err := json.Marshal(a)", "if err != nil {", "return nil, err", "}", "return withExtra(named, v.Tags)"},
		},
		{
			// The bytes beside an error differ.
			name:  "the error branch returns the bytes",
			guard: "if len(extra) == 0 {\n\t\treturn named, nil\n\t}",
			body:  "named, err := json.Marshal(a)\n\t\tif err != nil {\n\t\t\treturn named, err\n\t\t}\n\t\treturn withExtra(named, v.Extra)",
			want:  []string{"named, err := json.Marshal(a)", "if err != nil {", "return named, err", "}", "return named, nil"},
		},
	}
	fset := token.NewFileSet()
	imp := importer.ForCompiler(fset, "source", nil)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			src := "package acp\n\nimport (\n\t\"encoding/json\"\n\t\"errors\"\n)\n\n" +
				"func encode(any) ([]byte, error) { return []byte(\"{}\"), errors.New(\"no\") }\n\n" +
				"type T struct {\n\tA     string\n\tTags  map[string]json.RawMessage\n\tExtra map[string]json.RawMessage\n}\n\n" +
				"func withExtra(named []byte, extra map[string]json.RawMessage) ([]byte, error) {\n\t" + tc.guard + "\n\treturn json.Marshal(extra)\n}\n\n" +
				"func (v T) MarshalJSON() ([]byte, error) {\n\ttype Alias T\n\ta := Alias(v)\n\t" + tc.body + "\n}\n"
			if err := os.WriteFile(filepath.Join(dir, "t_gen.go"), []byte(src), 0o644); err != nil {
				t.Fatal(err)
			}
			s, err := typecheck(fset, imp, t.TempDir(), dir)
			if err != nil {
				t.Fatal(err)
			}
			p := &projection{
				info: s.info, scope: s.pkg.Scope(), funcs: s.funcs,
				stableFields: map[string]bool{"A": true, "Tags": true}, addedFields: map[string]bool{"Extra": true},
			}
			body := s.funcs["T.MarshalJSON"].Body
			p.emptyAdded(body)
			s.foldMarshal(body)
			want := append([]string{"{", "type Alias T", "a := Alias(v)"}, tc.want...)
			if got := s.text(body); !slices.Equal(got, append(want, "}")) {
				t.Errorf("got %q, want %q", got, append(want, "}"))
			}
		})
	}
}
