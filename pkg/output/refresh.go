package output

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// ReadFilterLine recovers the filter recorded in a file WriteFile produced,
// so --refresh can re-run it without retyping.
func ReadFilterLine(path string) (string, error) {
	f, err := os.Open(path) // #nosec G304 -- a path the user named
	if err != nil {
		return "", err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if rest, ok := strings.CutPrefix(line, FilterMarker); ok {
			return strings.TrimSpace(rest), nil
		}
		// The marker is written first; stop at the first section header.
		if strings.HasPrefix(strings.TrimSpace(line), "[") {
			break
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf(
		"%s has no %sline, so faws will not touch it. Either faws did not write it, or it was written with --format json — faws cannot mark json files, because a \"#\" line would make them invalid JSON",
		path, FilterMarker)
}
