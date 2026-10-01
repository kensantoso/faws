package output

import (
	"strings"
	"time"
)

// ExpiryClause describes when expiration (an RFC3339 timestamp, the shape
// vend.Creds.Expiration carries) occurs relative to now, rounded to the
// nearest minute, for the post-write summary line
// ("wrote ./agent.config (0600) · 3 profile(s) · expires in 58m").
//
// It returns "" — never an error — when expiration is empty or fails to
// parse as RFC3339, so a missing or malformed timestamp is silently omitted
// from the summary rather than reported as a bogus duration.
func ExpiryClause(expiration string, now time.Time) string {
	if expiration == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, expiration)
	if err != nil {
		return ""
	}
	if !t.After(now) {
		return "expired"
	}
	return "expires in " + formatDuration(t.Sub(now))
}

// formatDuration rounds d to the nearest minute and renders it without the
// trailing ":00s" seconds component Duration.String always appends
// (Round(time.Minute) guarantees the seconds component is exactly zero, so
// the string always ends in a literal "0s").
func formatDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	if d <= 0 {
		// Sub-30-second remainder rounds down to zero, but the caller only
		// reaches here when the deadline is still in the future — show the
		// smallest displayable unit rather than "expired".
		d = time.Minute
	}
	return strings.TrimSuffix(d.String(), "0s")
}
