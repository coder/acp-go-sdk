package roundtrip

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// A patch field is an optional property of an update that changes a value
// the receiver stores: absent leaves the value as it is, null clears it, and
// a value replaces it, so [] clears an array. The schema says so only in its
// descriptions. These patterns read them, as cmd/generate/internal/ir does:
// an object whose description says that its optional properties are all
// patch fields (changedOnly or patchedObject), an object description that
// names its patch fields in a "have patch semantics" sentence, and a property
// whose description says that omitted means unchanged or that null clears it.
// Null clears only where the text says so (nullClears): in an object that
// says only that it carries the changed fields, as ToolCallUpdate does, ACP
// v1 reads null as absent, as the Rust SDK does.
var (
	changedOnly    = regexp.MustCompile(`(?i)only changed fields|partial updates?\b`)
	patchedObject  = regexp.MustCompile(`(?i)omitted patch fields`)
	patchSemantics = regexp.MustCompile("(?i)[^.]*\\bhave patch semantics")
	backticked     = regexp.MustCompile("`([^`]+)`")
	patchProperty  = regexp.MustCompile("(?i)omitt(?:ed|ing)[^.]*\\bunchanged\\b|\\bnull`? (?:clears|removes)\\b|set to null to clear")
	nullClears     = regexp.MustCompile("(?i)\\bnull`? (?:clears|removes)\\b|set to null to clear")
)

// patchField is the schema of a patch field, and whether null clears it.
type patchField struct {
	schema map[string]any
	clears bool
}

// patchSite is a patch field in a value that the SDK reads as def: in the
// def itself, or in one of its top-level alternatives (variant >= 0).
type patchSite struct {
	file    *schemaFile
	def     string
	variant int
	prop    string
	patchField
}

func (site patchSite) path() string {
	if site.variant < 0 {
		return site.def + "." + site.prop
	}
	return fmt.Sprintf("%s[%d].%s", site.def, site.variant, site.prop)
}

// patchFieldsOf returns the patch fields of the object that node describes,
// following $ref and allOf, each with its schema.
func patchFieldsOf(defs map[string]any, node any) map[string]patchField {
	fields := map[string]patchField{}
	seen := map[string]bool{}
	var walk func(node any)
	walk = func(node any) {
		m, _ := node.(map[string]any)
		desc, _ := m["description"].(string)
		named := map[string]bool{}
		for _, sentence := range patchSemantics.FindAllString(desc, -1) {
			for _, n := range backticked.FindAllStringSubmatch(sentence, -1) {
				named[n[1]] = true
			}
		}
		var required []string
		names, _ := m["required"].([]any)
		for _, n := range names {
			if n, ok := n.(string); ok {
				required = append(required, n)
			}
		}
		props, _ := m["properties"].(map[string]any)
		for name, p := range props {
			ps, _ := p.(map[string]any)
			pdesc, _ := ps["description"].(string)
			if slices.Contains(required, name) {
				continue
			}
			patched := patchedObject.MatchString(desc) || patchSemantics.MatchString(desc)
			if changedOnly.MatchString(desc) || patched || named[name] || patchProperty.MatchString(pdesc) {
				if _, ok := fields[name]; !ok {
					fields[name] = patchField{ps, patched || !changedOnly.MatchString(desc) || nullClears.MatchString(pdesc)}
				}
			}
		}
		parts, _ := m["allOf"].([]any)
		for _, part := range parts {
			walk(part)
		}
		if ref, ok := m["$ref"].(string); ok && !seen[ref] {
			seen[ref] = true
			walk(defs[strings.TrimPrefix(ref, "#/$defs/")])
		}
	}
	walk(node)
	return fields
}

// findPatches lists the patch fields of each schema file, in each def and in
// each top-level alternative of a def, as findHints lists hinted properties.
func findPatches(s *suite) []patchSite {
	var sites []patchSite
	add := func(f *schemaFile, def string, variant int, node any) {
		fields := patchFieldsOf(f.defs, node)
		for _, prop := range sortedKeys(fields) {
			sites = append(sites, patchSite{f, def, variant, prop, fields[prop]})
		}
	}
	for _, f := range s.files {
		for _, def := range sortedKeys(f.defs) {
			add(f, def, -1, f.defs[def])
			m, _ := f.defs[def].(map[string]any)
			for _, key := range []string{"anyOf", "oneOf"} {
				alts, _ := m[key].([]any)
				for i, alt := range alts {
					add(f, def, i, alt)
				}
			}
		}
	}
	return sites
}

// TestPatches checks that the SDK keeps the three forms of each patch field
// apart: an absent member stays absent, null stays null, and an empty array
// stays an empty array, so that a receiver can tell "unchanged" from "clear".
// Where null does not clear, it means "unchanged" too, so null must come back
// absent, as the Rust SDK writes it.
func TestPatches(t *testing.T) {
	s := load(t)
	sites := findPatches(s)
	counts := map[string]int{}
	for _, site := range sites {
		name := fmt.Sprintf("%s/%s", site.file.name, site.path())
		outcome := "pass"
		t.Run(name, func(t *testing.T) {
			skip, problems := checkPatch(site)
			switch {
			case skip != "":
				outcome = "skip"
				t.Logf("skip %s: %s", name, skip)
			case len(problems) > 0:
				outcome = "fail"
				for _, p := range problems {
					t.Errorf("PATCH %s (%s) [%s]: %s", site.path(), site.file.name, p.probe, p.detail)
				}
			}
		})
		counts[outcome]++
	}
	t.Logf("SUMMARY patches: %d sites, %d pass, %d fail, %d skipped (no Go type, or no instance)",
		len(sites), counts["pass"], counts["fail"], counts["skip"])
}

// checkPatch decodes and encodes the def's value with the patch field
// absent, null, and, for an array, empty, and returns a problem for each form
// that the SDK does not write back: null as null where it clears, and as
// absent where it does not.
func checkPatch(site patchSite) (string, []hintProblem) {
	if _, ok := registry[site.def]; !ok {
		return "no SDK type", nil
	}
	base, ok := instanceGen{defs: site.file.defs}.value(site.file.defs[site.def], 0, site.variant)
	obj, isObj := base.(map[string]any)
	if !ok || !isObj {
		return "no object instance", nil
	}
	forms := []struct {
		probe string
		value any
		want  string // the member in the output; "" for none
	}{{"absent", nil, ""}, {"null", nil, "null"}}
	if !site.clears {
		forms[1].want = ""
	}
	if strings.Contains(fmt.Sprint(site.schema["type"]), "array") {
		forms = append(forms, struct {
			probe string
			value any
			want  string
		}{"empty", []any{}, "[]"})
	}
	var problems []hintProblem
	for _, form := range forms {
		o := map[string]any{}
		for k, v := range obj {
			if k != site.prop {
				o[k] = v
			}
		}
		if form.probe != "absent" {
			o[site.prop] = form.value
		}
		input, _ := json.Marshal(o)
		if site.file.validate(site.def, input) != nil {
			continue
		}
		out, err := decodeEncode(site.def, input)
		if err != nil {
			problems = append(problems, hintProblem{form.probe, fmt.Sprintf("decode %s: %v", oneLine(string(input), 160), oneLine(err.Error(), 160))})
			continue
		}
		var members map[string]json.RawMessage
		if err := json.Unmarshal(out, &members); err != nil {
			problems = append(problems, hintProblem{form.probe, "the output is no object: " + oneLine(string(out), 160)})
			continue
		}
		got, present := members[site.prop]
		switch {
		case form.want == "" && present:
			problems = append(problems, hintProblem{form.probe, fmt.Sprintf("%s %s comes back as %s, want it absent", form.probe, site.prop, got)})
		case form.want != "" && string(got) != form.want:
			if !present {
				got = json.RawMessage("absent")
			}
			problems = append(problems, hintProblem{form.probe, fmt.Sprintf("%s %s comes back as %s", form.want, site.prop, got)})
		}
	}
	return "", problems
}

// The patterns find each kind of patch field and nothing else.
func TestPatchFieldsOf_ReadsTheDescriptions(t *testing.T) {
	var defs map[string]any
	if err := json.Unmarshal([]byte(`{
		"Update": {"description": "All fields except the id are optional - only changed fields need to be included.", "type": "object", "required": ["id"],
			"properties": {"id": {"type": "string"}, "title": {"type": ["string", "null"]}}},
		"Named": {"description": "`+"`summary` and `_meta` have patch semantics: omission leaves it unchanged."+`", "type": "object",
			"properties": {"summary": {"type": ["array", "null"]}, "_meta": {"type": ["object", "null"]}, "status": {"type": "string"}}},
		"Own": {"type": "object", "properties": {
			"cwd": {"type": ["string", "null"], "description": "Omitted means unchanged. `+"`null`"+` clears it."},
			"title": {"type": ["string", "null"], "description": "Set to null to clear."},
			"name": {"type": ["string", "null"], "description": "The name."}}},
		"Variant": {"allOf": [{"$ref": "#/$defs/Update"}], "properties": {"kind": {"const": "update"}}},
		"Info": {"description": "All fields are optional to support partial updates.", "type": "object",
			"properties": {"title": {"type": ["string", "null"], "description": "Set to null to clear."}, "_meta": {"type": ["object", "null"]}}}
	}`), &defs); err != nil {
		t.Fatal(err)
	}
	// A field marked + is one where null clears.
	for def, want := range map[string]string{"Update": "title", "Named": "_meta+ summary+", "Own": "cwd+ title+", "Variant": "title", "Info": "_meta title+"} {
		fields := patchFieldsOf(defs, defs[def])
		var got []string
		for _, name := range sortedKeys(fields) {
			if fields[name].clears {
				name += "+"
			}
			got = append(got, name)
		}
		if strings.Join(got, " ") != want {
			t.Errorf("patch fields of %s = %q, want %q", def, strings.Join(got, " "), want)
		}
	}
}
