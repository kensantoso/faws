package cmd

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/kensantoso/faws/pkg/output"
	"github.com/kensantoso/faws/pkg/vend"
)

func TestParseInvocationLine(t *testing.T) {
	got, err := parseInvocationLine("--all a,b --any c --exclude prod --exclude dev")
	if err != nil {
		t.Fatalf("parseInvocationLine: %v", err)
	}
	if strings.Join(got.all, "|") != "a,b" {
		t.Fatalf("all: %v", got.all)
	}
	if strings.Join(got.any, "|") != "c" {
		t.Fatalf("any: %v", got.any)
	}
	if strings.Join(got.exclude, "|") != "prod|dev" {
		t.Fatalf("exclude: %v", got.exclude)
	}
}

func TestParseInvocationLineRejectsUnknownFlag(t *testing.T) {
	if _, err := parseInvocationLine("--nope x"); err == nil {
		t.Fatal("unknown flag must error")
	}
}

func TestParseInvocationLineRoundTrips(t *testing.T) {
	line := invocationLine(opts{all: []string{"a,b"}, any: []string{"c"}, exclude: []string{"prod"}})
	parsed, err := parseInvocationLine(line)
	if err != nil {
		t.Fatalf("parseInvocationLine: %v", err)
	}
	if invocationLine(parsed) != line {
		t.Fatalf("round trip lost information: %q", invocationLine(parsed))
	}
}

// TestParseInvocationLineRoundTripsFormatRegionOutputAndSet covers FIX 2:
// --format/--region/--output/-c must survive the round trip too, not just
// the filter terms, or a file written with a non-default format/region
// refreshes back silently wrong.
func TestParseInvocationLineRoundTripsFormatRegionOutputAndSet(t *testing.T) {
	line := invocationLine(opts{
		all:       []string{"readonly"},
		format:    "credentials",
		region:    "ap-southeast-2",
		outputFmt: "json",
		set:       []string{"role_session_name=agent", "duration_seconds=3600"},
	})
	parsed, err := parseInvocationLine(line)
	if err != nil {
		t.Fatalf("parseInvocationLine: %v", err)
	}
	if parsed.format != "credentials" {
		t.Fatalf("format: got %q", parsed.format)
	}
	if parsed.region != "ap-southeast-2" {
		t.Fatalf("region: got %q", parsed.region)
	}
	if parsed.outputFmt != "json" {
		t.Fatalf("outputFmt: got %q", parsed.outputFmt)
	}
	if strings.Join(parsed.set, "|") != "role_session_name=agent|duration_seconds=3600" {
		t.Fatalf("set: got %v", parsed.set)
	}
	if invocationLine(parsed) != line {
		t.Fatalf("round trip lost information: %q", invocationLine(parsed))
	}
}

// TestParseInvocationLineRoundTripsDefault covers FIX A: --default must
// survive the round trip too, not just the filter terms and
// --format/--region/--output/-c — or a file vended with --default silently
// loses its [default] block on the first --refresh, with no error.
func TestParseInvocationLineRoundTripsDefault(t *testing.T) {
	line := invocationLine(opts{
		all:       []string{"acme-dev"},
		defaultTo: "acme-dev/ReadOnly",
	})
	parsed, err := parseInvocationLine(line)
	if err != nil {
		t.Fatalf("parseInvocationLine: %v", err)
	}
	if parsed.defaultTo != "acme-dev/ReadOnly" {
		t.Fatalf("defaultTo: got %q", parsed.defaultTo)
	}
	if invocationLine(parsed) != line {
		t.Fatalf("round trip lost information: %q", invocationLine(parsed))
	}
}

// TestFilterSurvivesWriteAndRefresh chains the whole --refresh path:
// invocationLine -> output.WriteFile -> output.ReadFilterLine ->
// parseInvocationLine must reproduce the original opts fields exactly, with
// no AWS calls involved.
func TestFilterSurvivesWriteAndRefresh(t *testing.T) {
	want := opts{
		all:       []string{"a,b"},
		any:       []string{"c"},
		exclude:   []string{"prod"},
		defaultTo: "acme-dev/ReadOnly",
		format:    "credentials",
		region:    "ap-southeast-2",
		outputFmt: "json",
		set:       []string{"role_session_name=agent"},
	}

	line := invocationLine(want)

	path := filepath.Join(t.TempDir(), "agent.config")
	creds := []vend.Creds{{Profile: "acme-dev/ReadOnly", AccessKeyID: "ASIA1", SecretAccessKey: "s1"}}
	if err := output.WriteFile(path, creds, output.Options{Format: "config", FilterLine: line}); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := output.ReadFilterLine(path)
	if err != nil {
		t.Fatalf("ReadFilterLine: %v", err)
	}
	if got != line {
		t.Fatalf("ReadFilterLine: got %q want %q", got, line)
	}

	parsed, err := parseInvocationLine(got)
	if err != nil {
		t.Fatalf("parseInvocationLine: %v", err)
	}
	if strings.Join(parsed.all, "|") != strings.Join(want.all, "|") {
		t.Fatalf("all: got %v want %v", parsed.all, want.all)
	}
	if strings.Join(parsed.any, "|") != strings.Join(want.any, "|") {
		t.Fatalf("any: got %v want %v", parsed.any, want.any)
	}
	if strings.Join(parsed.exclude, "|") != strings.Join(want.exclude, "|") {
		t.Fatalf("exclude: got %v want %v", parsed.exclude, want.exclude)
	}
	if parsed.defaultTo != want.defaultTo {
		t.Fatalf("defaultTo: got %q want %q", parsed.defaultTo, want.defaultTo)
	}
	if parsed.format != want.format {
		t.Fatalf("format: got %q want %q", parsed.format, want.format)
	}
	if parsed.region != want.region {
		t.Fatalf("region: got %q want %q", parsed.region, want.region)
	}
	if parsed.outputFmt != want.outputFmt {
		t.Fatalf("outputFmt: got %q want %q", parsed.outputFmt, want.outputFmt)
	}
	if strings.Join(parsed.set, "|") != strings.Join(want.set, "|") {
		t.Fatalf("set: got %v want %v", parsed.set, want.set)
	}
}
