// Package listing renders the default (no -o) output: what the filter
// selected, and why.
package listing

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/kensantoso/faws/pkg/filter"
)

// maxDisplayNameWidth caps how many runes of a profile name text/tabwriter
// is allowed to see. tabwriter pads every cell in a column to the width of
// that column's widest cell, so a single pathologically long name (AWS
// places no length limit on profile names) would otherwise stretch every
// row in the listing, not just its own. 60 runes comfortably fits the
// conventional "account-or-org/RoleName" shape used throughout this tool's
// own fixtures while still catching genuine outliers. This is display-only:
// the full name is still what gets vended and written to output files.
const maxDisplayNameWidth = 60

// ellipsis is a single rune so width arithmetic in displayName stays exact:
// the truncated string is always exactly maxDisplayNameWidth runes long.
const ellipsis = '…'

// displayName returns name unchanged if it fits within maxDisplayNameWidth
// runes. Otherwise it elides the middle, not the tail: AWS profile names
// conventionally carry the account/org at the front and the role after a
// "/" at the end, and both ends carry information a tail truncation would
// destroy (the role, in particular, is usually the most useful part).
//
// Truncation counts runes, not bytes, so multi-byte profile names are
// never cut mid-rune.
func displayName(name string) string {
	runes := []rune(name)
	if len(runes) <= maxDisplayNameWidth {
		return name
	}

	budget := maxDisplayNameWidth - 1 // one rune reserved for the ellipsis

	// Prefer keeping the trailing "/Role" segment intact.
	suffixLen := budget / 2
	if i := lastSlashIndex(runes); i >= 0 {
		if tail := len(runes) - i; tail > 0 && tail < budget {
			suffixLen = tail
		}
	}
	prefixLen := budget - suffixLen

	prefix := string(runes[:prefixLen])
	suffix := string(runes[len(runes)-suffixLen:])
	return prefix + string(ellipsis) + suffix
}

// lastSlashIndex returns the rune index of the last '/' in runes, or -1.
func lastSlashIndex(runes []rune) int {
	for i := len(runes) - 1; i >= 0; i-- {
		if runes[i] == '/' {
			return i
		}
	}
	return -1
}

// Render writes one line per match plus a count. Columns are best-effort —
// a static-key profile has no account or role, and that is shown as blank
// rather than treated as an error.
func Render(w io.Writer, matches []filter.Match) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, m := range matches {
		why := ""
		if len(m.Keys) > 0 {
			why = "← " + strings.Join(m.Keys, ", ")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			displayName(m.Profile.Name), m.Profile.AccountID(), m.Profile.RoleName(), m.Profile.Region(), why)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	noun := "profiles"
	if len(matches) == 1 {
		noun = "profile"
	}
	_, err := fmt.Fprintf(w, "%d %s\n", len(matches), noun)
	return err
}
