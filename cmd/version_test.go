package cmd

import (
	"runtime/debug"
	"testing"
)

func TestVersionPrefersLinkerValue(t *testing.T) {
	bi := &debug.BuildInfo{Main: debug.Module{Version: "v0.1.0"}}
	if got := resolveVersion("v9.9.9", bi, true); got != "v9.9.9" {
		t.Fatalf("got %q", got)
	}
}

func TestVersionFallsBackToModuleVersion(t *testing.T) {
	bi := &debug.BuildInfo{Main: debug.Module{Version: "v0.1.0"}}
	if got := resolveVersion("dev", bi, true); got != "v0.1.0" {
		t.Fatalf("a go install build must report its module version, got %q", got)
	}
}

func TestVersionStaysDevForLocalBuilds(t *testing.T) {
	bi := &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}
	if got := resolveVersion("dev", bi, true); got != "dev" {
		t.Fatalf("got %q", got)
	}
	if got := resolveVersion("dev", nil, false); got != "dev" {
		t.Fatalf("got %q", got)
	}
}
