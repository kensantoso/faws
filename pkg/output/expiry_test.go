package output

import (
	"testing"
	"time"
)

func TestExpiryClauseFutureTime(t *testing.T) {
	now := time.Date(2026, 9, 23, 18, 0, 0, 0, time.UTC)
	exp := now.Add(58 * time.Minute).Format(time.RFC3339)
	got := ExpiryClause(exp, now)
	if got != "expires in 58m" {
		t.Fatalf("got %q want %q", got, "expires in 58m")
	}
}

func TestExpiryClauseEmpty(t *testing.T) {
	if got := ExpiryClause("", time.Now()); got != "" {
		t.Fatalf("empty expiration must be omitted, not errored, got %q", got)
	}
}

func TestExpiryClauseUnparseable(t *testing.T) {
	if got := ExpiryClause("not-a-timestamp", time.Now()); got != "" {
		t.Fatalf("unparseable expiration must be omitted, not a bogus duration, got %q", got)
	}
}

func TestExpiryClauseAlreadyPast(t *testing.T) {
	now := time.Date(2026, 9, 23, 18, 0, 0, 0, time.UTC)
	exp := now.Add(-5 * time.Minute).Format(time.RFC3339)
	got := ExpiryClause(exp, now)
	if got != "expired" {
		t.Fatalf("got %q want %q", got, "expired")
	}
}

func TestExpiryClauseRoundsToNearestMinute(t *testing.T) {
	now := time.Date(2026, 9, 23, 18, 0, 0, 0, time.UTC)
	exp := now.Add(90 * time.Minute).Format(time.RFC3339)
	if got := ExpiryClause(exp, now); got != "expires in 1h30m" {
		t.Fatalf("got %q want %q", got, "expires in 1h30m")
	}
}
