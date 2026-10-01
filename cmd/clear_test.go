package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kensantoso/faws/pkg/output"
)

func TestClearRemovesAFawsFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "agent.config")
	body := output.FilterMarker + "--all readonly\n\n[profile x]\naws_access_key_id = ASIA\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "--clear", p); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("file should be gone")
	}
}

func TestClearRefusesAFileWithNoMarker(t *testing.T) {
	p := filepath.Join(t.TempDir(), "credentials")
	if err := os.WriteFile(p, []byte("[default]\naws_access_key_id = REAL\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := run(t, "--clear", p)
	if err == nil {
		t.Fatal("must refuse a file with no marker")
	}
	// The error must not claim faws didn't write the file — it can't know
	// that, and a --format json file faws DID write also has no marker.
	if strings.Contains(err.Error(), "not written by faws") {
		t.Fatalf("error must not falsely assert provenance, got %v", err)
	}
	if !strings.Contains(err.Error(), output.FilterMarker) {
		t.Fatalf("error should explain why, got %v", err)
	}
	if _, statErr := os.Stat(p); statErr != nil {
		t.Fatal("file must be left alone")
	}
}
