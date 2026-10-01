package vend

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// Result is one profile's outcome from VendPool.
type Result struct {
	Index   int
	Profile string
	Creds   Creds
	Err     error
}

// errStopped marks a profile that never ran because the pool stopped first.
var errStopped = errors.New("not vended: stopped after an earlier failure")

// IsStopped reports whether err means the profile was never attempted.
func IsStopped(err error) bool { return errors.Is(err, errStopped) }

// VendPool vends non-interactive profiles with up to workers at a time.
// Results come back in input order; onDone (may be nil) fires once per
// profile, serialized, in completion order. With stopOnError, the first
// failure cancels in-flight vends and skips the rest. Never pass a profile
// that may prompt: concurrent MFA prompts would interleave.
func VendPool(ctx context.Context, profiles []string, workers int, timeoutFor func(string) time.Duration, stopOnError bool, onDone func(Result)) []Result {
	if workers < 1 {
		workers = 1
	}
	if workers > len(profiles) {
		workers = len(profiles)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make([]Result, len(profiles))
	var mu sync.Mutex
	failed := false
	record := func(r Result) {
		mu.Lock()
		defer mu.Unlock()
		results[r.Index] = r
		if onDone != nil {
			onDone(r)
		}
	}

	jobs := make(chan int, len(profiles))
	for i := range profiles {
		jobs <- i
	}
	close(jobs)

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for i := range jobs {
				p := profiles[i]
				mu.Lock()
				stop := failed
				mu.Unlock()
				if stop {
					record(Result{Index: i, Profile: p, Err: errStopped})
					continue
				}
				var timeout time.Duration
				if timeoutFor != nil {
					timeout = timeoutFor(p)
				}
				c, err := Vend(ctx, p, strings.NewReader(""), timeout, false)
				if err != nil && stopOnError {
					mu.Lock()
					first := !failed
					failed = true
					mu.Unlock()
					if first {
						cancel()
					} else {
						err = errStopped
					}
				}
				record(Result{Index: i, Profile: p, Creds: c, Err: err})
			}
		}()
	}
	wg.Wait()
	return results
}
