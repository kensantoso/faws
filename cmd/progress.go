package cmd

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
)

// vendProgress shows per-profile vend status. i is the profile's position in
// the names the progress was built with.
type vendProgress interface {
	done(i int, name string, err error)
}

// newProgress picks the live block on a terminal and ordered lines elsewhere.
func newProgress(w io.Writer, names []string, allowLive bool) vendProgress {
	if allowLive {
		if width, rows, ok := terminalSize(w); ok && liveFits(len(names), rows) {
			return newLiveProgress(w, names, width)
		}
	}
	return &lineProgress{w: w, names: names, results: map[int]error{}, seen: map[int]bool{}}
}

// lineProgress prints "[k/N] name" in input order, holding results that
// finish early, so logs stay deterministic under parallel vending.
type lineProgress struct {
	mu      sync.Mutex
	w       io.Writer
	names   []string
	results map[int]error
	seen    map[int]bool
	next    int
}

func (p *lineProgress) done(i int, _ string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.results[i], p.seen[i] = err, true
	for p.seen[p.next] {
		line := fmt.Sprintf("  [%d/%d] %s", p.next+1, len(p.names), p.names[p.next])
		if p.results[p.next] != nil {
			line += "  ✗ failed"
		}
		fmt.Fprintln(p.w, line)
		p.next++
	}
}

// liveProgress draws every profile at once and rewrites each line in place
// as its vend finishes.
type liveProgress struct {
	mu    sync.Mutex
	w     io.Writer
	names []string
	width int
}

func newLiveProgress(w io.Writer, names []string, width int) *liveProgress {
	p := &liveProgress{w: w, names: names, width: width}
	for _, n := range names {
		fmt.Fprintln(w, p.fit("  … "+n))
	}
	return p
}

func (p *liveProgress) done(i int, name string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	line := "  ✓ " + name
	if err != nil {
		line = "  ✗ " + name + "  failed"
	}
	up := len(p.names) - i
	// Up to line i, clear it, redraw, then back below the block.
	fmt.Fprintf(p.w, "\033[%dA\r\033[2K%s\033[%dB\r", up, p.fit(line), up)
}

// fit keeps a line under the terminal width, since a wrapped line would
// break the cursor arithmetic.
func (p *liveProgress) fit(s string) string {
	r := []rune(s)
	if p.width > 1 && len(r) > p.width-1 {
		return string(r[:p.width-2]) + "…"
	}
	return s
}

var (
	ansiRE       = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	vendPrefixRE = regexp.MustCompile(`^vend "[^"]*":?\s*(exit status \d+:\s*)?`)
)

// skipReason turns a vend error into one readable line: no profile prefix,
// no exit status, no resolver colour codes.
func skipReason(err error) string {
	s := ansiRE.ReplaceAllString(err.Error(), "")
	s = vendPrefixRE.ReplaceAllString(s, "")
	s = strings.TrimPrefix(s, "aws: ")
	s = strings.TrimPrefix(s, "[ERROR]: ")
	return strings.Join(strings.Fields(s), " ")
}

// isTerminal reports whether w is a character device, as a terminal is.
func isTerminal(w io.Writer) (*os.File, bool) {
	f, ok := w.(*os.File)
	if !ok {
		return nil, false
	}
	fi, err := f.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 || os.Getenv("TERM") == "dumb" {
		return nil, false
	}
	return f, true
}

// liveFits reports whether n lines plus the cursor line fit on screen;
// cursor-up cannot reach a line that has scrolled off.
func liveFits(n, rows int) bool { return n < rows }
