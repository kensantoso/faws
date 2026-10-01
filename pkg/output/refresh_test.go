package output

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kensantoso/faws/pkg/vend"
)

func TestReadFilterLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "agent.config")
	body := FilterMarker + "--all readonly --exclude prod\n\n[profile x]\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFilterLine(p)
	if err != nil {
		t.Fatalf("ReadFilterLine: %v", err)
	}
	if got != "--all readonly --exclude prod" {
		t.Fatalf("got %q", got)
	}
}

func TestReadFilterLineMissingMarker(t *testing.T) {
	p := filepath.Join(t.TempDir(), "plain.config")
	if err := os.WriteFile(p, []byte("[profile x]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFilterLine(p); err == nil {
		t.Fatal("a file with no marker must error")
	}
}

// TestEnvFormatRoundTripsThroughReadFilterLine covers FIX 3: "#" is a valid
// shell comment, so unlike json, an env-format file CAN carry the marker and
// be refreshed/cleared.
func TestEnvFormatRoundTripsThroughReadFilterLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.env")
	line := "--all readonly"
	oneCred := []vend.Creds{{Profile: "acme-dev/ReadOnly", AccessKeyID: "ASIA1", SecretAccessKey: "s1"}}
	if err := WriteFile(path, oneCred, Options{Format: "env", FilterLine: line}); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := ReadFilterLine(path)
	if err != nil {
		t.Fatalf("ReadFilterLine on an env file must succeed: %v", err)
	}
	if got != line {
		t.Fatalf("got %q want %q", got, line)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "export AWS_ACCESS_KEY_ID=ASIA1") {
		t.Fatalf("env body missing after adding the marker:\n%s", b)
	}
}

// TestJSONFormatCannotBeRefreshedWithAnAccurateError covers FIX 3: json
// files never carry the marker (a "#" line would be invalid JSON), and the
// resulting error must say so accurately rather than claim faws didn't write
// the file.
func TestJSONFormatCannotBeRefreshedWithAnAccurateError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := WriteFile(path, creds(), Options{Format: "json", FilterLine: "--all readonly"}); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := ReadFilterLine(path)
	if err == nil {
		t.Fatal("a json file must never be readable as refreshable")
	}
	if strings.Contains(err.Error(), "not written by faws") {
		t.Fatalf("error must not falsely claim faws did not write the file: %v", err)
	}
	if !strings.Contains(err.Error(), "json") {
		t.Fatalf("error must explain that json output cannot be marked: %v", err)
	}
}

func TestWriteFileFilterLineRoundTripsIncludingEmpty(t *testing.T) {
	for _, line := range []string{"--all readonly --exclude prod", ""} {
		path := filepath.Join(t.TempDir(), "agent.config")
		if err := WriteFile(path, creds(), Options{Format: "config", FilterLine: line}); err != nil {
			t.Fatalf("WriteFile(%q): %v", line, err)
		}
		got, err := ReadFilterLine(path)
		if err != nil {
			t.Fatalf("ReadFilterLine after WriteFile(%q): %v", line, err)
		}
		if got != line {
			t.Fatalf("round trip: wrote %q, read back %q", line, got)
		}
	}
}
