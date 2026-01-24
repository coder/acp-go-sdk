// Package util contains small string and identifier helpers used by the
// code generator for formatting names and comments.
package util

import (
	"strings"
	"unicode"
)

// SanitizeComment removes backticks and normalizes whitespace for Go comments.
// Deprecated: Use FormatDocComment for proper multi-line comment formatting.
func SanitizeComment(s string) string {
	s = strings.ReplaceAll(s, "`", "'")
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	return strings.Join(lines, " ")
}

// FormatDocComment formats a description as properly structured Go doc comment lines.
// Preserves paragraph breaks (double newlines) and handles line breaks within paragraphs.
// Returns slice of comment text without "//" prefix (caller should emit each as a comment line).
//
// Go doc comment conventions:
//   - Each line of comment text becomes a separate "// line"
//   - Blank lines (from \n\n) become "//" with no text
//   - First line should be a complete sentence ending with period
func FormatDocComment(desc string) []string {
	if desc == "" {
		return nil
	}

	// Replace backticks with single quotes (Go doc comments don't support backticks well)
	desc = strings.ReplaceAll(desc, "`", "'")

	// Split into lines based on newlines from the JSON schema
	lines := strings.Split(desc, "\n")

	var result []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Preserve blank lines as empty strings - jen will render them as "//"
		result = append(result, trimmed)
	}

	return result
}

// TitleWord uppercases the first rune and lowercases the rest.
func TitleWord(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	for i := 1; i < len(r); i++ {
		r[i] = unicode.ToLower(r[i])
	}
	return string(r)
}

// SplitCamel splits a camelCase string into tokens.
func SplitCamel(s string) []string {
	var parts []string
	last := 0
	for i := 1; i < len(s); i++ {
		if isBoundary(s[i-1], s[i]) {
			parts = append(parts, s[last:i])
			last = i
		}
	}
	parts = append(parts, s[last:])
	return parts
}

func isBoundary(prev, curr byte) bool {
	return (prev >= 'a' && prev <= 'z' && curr >= 'A' && curr <= 'Z') || curr == '_'
}

// ToExportedField converts a schema identifier into a Go-exported PascalCase identifier.
//
// The ACP schema is generally snake_case or camelCase, but some titles/descriptions can
// contain spaces or punctuation. We defensively treat any non-alphanumeric rune as a word
// separator.
func ToExportedField(name string) string {
	// Treat any non-alphanumeric rune as a separator.
	cleaned := make([]rune, 0, len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			cleaned = append(cleaned, r)
		case r >= 'A' && r <= 'Z':
			cleaned = append(cleaned, r)
		case r >= '0' && r <= '9':
			cleaned = append(cleaned, r)
		default:
			cleaned = append(cleaned, '_')
		}
	}

	// Split on separators, then split camelCase chunks within each token.
	var tokens []string
	for _, part := range strings.Split(string(cleaned), "_") {
		if part == "" {
			continue
		}
		for _, chunk := range SplitCamel(part) {
			if chunk == "" {
				continue
			}
			tokens = append(tokens, chunk)
		}
	}

	for i := range tokens {
		tokens[i] = TitleWord(tokens[i])
	}
	return strings.Join(tokens, "")
}

// ToEnumConst builds a const identifier like <TypeName><Value>.
func ToEnumConst(typeName, val string) string {
	cleaned := make([]rune, 0, len(val))
	for _, r := range val {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			cleaned = append(cleaned, r)
		} else {
			cleaned = append(cleaned, '_')
		}
	}
	parts := strings.FieldsFunc(string(cleaned), func(r rune) bool { return r == '_' })
	for i := range parts {
		parts[i] = TitleWord(strings.ToLower(parts[i]))
	}
	return typeName + strings.Join(parts, "")
}
