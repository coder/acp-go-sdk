package load

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadSchemaVersion(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
		wantErr bool
	}{
		{name: "trailing newline", content: "1.32.0\n", want: "1.32.0"},
		{name: "empty", content: "\n", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "version"), []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := ReadSchemaVersion(dir)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ReadSchemaVersion() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("ReadSchemaVersion() = %q, want %q", got, tt.want)
			}
		})
	}
}
