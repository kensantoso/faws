//go:build !windows

package vend

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// writeFakeAWSPool puts a fake "aws" on PATH whose export-credentials sleeps
// for the number of tenths given by the profile's "sleepN" suffix and fails
// for any profile containing "bad". Each vend touches a marker in logDir.
func writeFakeAWSPool(t *testing.T) (logDir string) {
	t.Helper()
	dir := t.TempDir()
	logDir = t.TempDir()
	script := `#!/bin/sh
profile=""
while [ $# -gt 0 ]; do
  if [ "$1" = "--profile" ]; then profile="$2"; fi
  shift
done
touch "` + logDir + `/$(echo "$profile" | tr / _)"
case "$profile" in
  *sleep*) n=${profile##*sleep}; sleep "0.$n" ;;
esac
case "$profile" in
  *bad*) echo "An error occurred (AccessDenied): no role for $profile" >&2; exit 253 ;;
esac
echo '{"Version":1,"AccessKeyId":"ASIAFAKE","SecretAccessKey":"s","SessionToken":"t","Expiration":"2099-01-01T00:00:00Z"}'
`
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logDir
}

func TestPoolReturnsResultsInInputOrder(t *testing.T) {
	writeFakeAWSPool(t)
	profiles := []string{"a-sleep5", "b-sleep1", "c-sleep3"}
	var mu sync.Mutex
	var completion []string
	res := VendPool(context.Background(), profiles, 3, nil, false, func(r Result) {
		mu.Lock()
		completion = append(completion, r.Profile)
		mu.Unlock()
	})
	for i, r := range res {
		if r.Index != i || r.Profile != profiles[i] || r.Err != nil || r.Creds.Profile != profiles[i] {
			t.Fatalf("result %d: %+v", i, r)
		}
	}
	if strings.Join(completion, ",") != "b-sleep1,c-sleep3,a-sleep5" {
		t.Fatalf("onDone must fire in completion order, got %v", completion)
	}
}

func TestPoolRunsConcurrently(t *testing.T) {
	writeFakeAWSPool(t)
	profiles := []string{"a-sleep5", "b-sleep5", "c-sleep5", "d-sleep5"}
	start := time.Now()
	VendPool(context.Background(), profiles, 4, nil, false, nil)
	if d := time.Since(start); d > 1500*time.Millisecond {
		t.Fatalf("4 x 0.5s vends with 4 workers took %v; not concurrent", d)
	}
}

func TestPoolRespectsWorkerLimit(t *testing.T) {
	writeFakeAWSPool(t)
	profiles := []string{"a-sleep5", "b-sleep5", "c-sleep5", "d-sleep5"}
	start := time.Now()
	VendPool(context.Background(), profiles, 2, nil, false, nil)
	if d := time.Since(start); d < 900*time.Millisecond {
		t.Fatalf("4 x 0.5s vends with 2 workers took %v; limit not applied", d)
	}
}

func TestPoolReportsEachFailureAndKeepsGoing(t *testing.T) {
	writeFakeAWSPool(t)
	res := VendPool(context.Background(), []string{"ok-1", "bad-2", "ok-3"}, 2, nil, false, nil)
	if res[0].Err != nil || res[2].Err != nil {
		t.Fatalf("good profiles must vend: %+v", res)
	}
	if res[1].Err == nil || !strings.Contains(res[1].Err.Error(), "AccessDenied") {
		t.Fatalf("bad profile must carry its error: %+v", res[1])
	}
}

func TestPoolStopOnErrorSkipsUnstartedProfiles(t *testing.T) {
	logDir := writeFakeAWSPool(t)
	res := VendPool(context.Background(), []string{"bad-1", "ok-sleep5", "ok-3", "ok-4"}, 1, nil, true, nil)
	if res[0].Err == nil {
		t.Fatal("first profile must fail")
	}
	for _, r := range res[1:] {
		if r.Err == nil {
			t.Fatalf("after a failure with stopOnError, %s must not succeed", r.Profile)
		}
	}
	if _, err := os.Stat(filepath.Join(logDir, "ok-4")); !os.IsNotExist(err) {
		t.Fatal("with stopOnError and 1 worker, later profiles must never start")
	}
	if !IsStopped(res[3].Err) {
		t.Fatalf("an unstarted profile must report it was stopped, got %v", res[3].Err)
	}
}

func TestPoolCancelStopsEverything(t *testing.T) {
	writeFakeAWSPool(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := VendPool(ctx, []string{"ok-1", "ok-2"}, 2, nil, false, nil)
	for _, r := range res {
		if r.Err == nil {
			t.Fatalf("a cancelled pool must not vend %s", r.Profile)
		}
	}
}
