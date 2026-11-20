package stablecheck

import (
	"go/importer"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestPruneInert(t *testing.T) {
	cases := []struct {
		name string
		body string
		// kept holds the lines that stay, without the braces of the body.
		kept []string
	}{
		{
			name: "the code around a removed switch on a new discriminator",
			body: `var disc string
				if v, ok := m["kind"]; ok {
					json.Unmarshal(v, &disc)
				}
				switch disc {
				}
				var arr []map[string]json.RawMessage
				if json.Unmarshal(b, &arr) == nil {
				}
				strings.ToUpper(tag)
				return nil`,
			kept: []string{"return nil"},
		},
		{
			name: "a statement that returns",
			body: `var disc string
				if v, ok := m["kind"]; ok {
					if err := json.Unmarshal(v, &disc); err != nil {
						return err
					}
				}
				return nil`,
			kept: []string{"var disc string", "if v, ok := m[\"kind\"]; ok {", "if err := json.Unmarshal(v, &disc); err != nil {", "return err", "}", "}", "return nil"},
		},
		{
			name: "a local that is read",
			body: `var n int
				json.Unmarshal(b, &n)
				return fmt.Errorf("%d", n)`,
			kept: []string{"var n int", "json.Unmarshal(b, &n)", "return fmt.Errorf(\"%d\", n)"},
		},
		{
			name: "a write of a local that is read",
			body: `failed := false
				if len(b) > 0 {
					failed = true
				}
				if failed {
					return errors.New("failed")
				}
				return nil`,
			kept: []string{"failed := false", "if len(b) > 0 {", "failed = true", "}", "if failed {", "return errors.New(\"failed\")", "}", "return nil"},
		},
		{
			name: "a statement that can panic or has an effect",
			body: `x := b[0]
				fmt.Println(tag)
				return nil`,
			kept: []string{"x := b[0]", "fmt.Println(tag)", "return nil"},
		},
		{
			// Without the case, "a" would go to the default case.
			name: "a case of a switch",
			body: `switch tag {
				case "a":
				default:
					return errors.New("other")
				}
				return nil`,
			kept: []string{"switch tag {", "case \"a\":", "default:", "return errors.New(\"other\")", "}", "return nil"},
		},
	}
	fset := token.NewFileSet()
	imp := importer.ForCompiler(fset, "source", nil)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			src := "package acp\n\nimport (\n\t\"encoding/json\"\n\t\"errors\"\n\t\"fmt\"\n\t\"strings\"\n)\n\n" +
				"var _, _, _, _ = json.Marshal, errors.New, fmt.Sprint, strings.ToUpper\n\n" +
				"func f(b []byte, m map[string]json.RawMessage, tag string) error {\n" + tc.body + "\n}\n"
			if err := os.WriteFile(filepath.Join(dir, "f_gen.go"), []byte(src), 0o644); err != nil {
				t.Fatal(err)
			}
			s, err := typecheck(fset, imp, t.TempDir(), dir)
			if err != nil {
				t.Fatal(err)
			}
			body := s.funcs["f"].Body
			s.pruneInert(body)
			want := append(append([]string{"{"}, tc.kept...), "}")
			if got := s.text(body); !slices.Equal(got, want) {
				t.Errorf("kept %q, want %q", got, want)
			}
		})
	}
}
