package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/kensantoso/faws/pkg/awsconfig"
	"github.com/kensantoso/faws/pkg/filter"
	"github.com/kensantoso/faws/pkg/vend"
)

func TestMFANotice(t *testing.T) {
	ms := []filter.Match{
		{Profile: awsconfig.Profile{Name: "a", Keys: map[string]string{"mfa_serial": "arn:aws:iam::1:mfa/ken"}}},
		{Profile: awsconfig.Profile{Name: "b", Keys: map[string]string{}}},
		{Profile: awsconfig.Profile{Name: "c", Keys: map[string]string{"mfa_serial": "arn:aws:iam::1:mfa/ken"}}},
	}
	got := mfaNotice(ms)
	if !strings.Contains(got, "2 of 3") {
		t.Fatalf("notice must count affected profiles, got %q", got)
	}
	if !strings.Contains(got, "prompted 2 times") {
		t.Fatalf("notice must state the prompt count, got %q", got)
	}
}

func TestMFANoticeEmptyWhenNoneNeedMFA(t *testing.T) {
	ms := []filter.Match{{Profile: awsconfig.Profile{Name: "b", Keys: map[string]string{}}}}
	if got := mfaNotice(ms); got != "" {
		t.Fatalf("want no notice, got %q", got)
	}
}

// TestMFAAwareTimeoutUsesMFATimeoutForMFAProfiles covers FIX 2: a profile
// carrying mfa_serial must get vend.MFATimeout, not the flat 20s (or
// whatever --timeout was set to) that used to SIGKILL an MFA prompt mid-entry.
func TestMFAAwareTimeoutUsesMFATimeoutForMFAProfiles(t *testing.T) {
	p := awsconfig.Profile{Name: "a", Keys: map[string]string{"mfa_serial": "arn:aws:iam::1:mfa/ken"}}
	got := mfaAwareTimeout(opts{timeout: 5 * time.Second}, p)
	if got != vend.MFATimeout {
		t.Fatalf("want vend.MFATimeout for an MFA profile, got %s", got)
	}
}

// TestMFAAwareTimeoutUsesFlagTimeoutForOrdinaryProfiles covers the other
// half: --timeout (the non-MFA case the brief asks for) must reach Vend for
// a profile with no mfa_serial, not be silently overridden.
func TestMFAAwareTimeoutUsesFlagTimeoutForOrdinaryProfiles(t *testing.T) {
	p := awsconfig.Profile{Name: "b", Keys: map[string]string{}}
	got := mfaAwareTimeout(opts{timeout: 90 * time.Second}, p)
	if got != 90*time.Second {
		t.Fatalf("want the --timeout value for a non-MFA profile, got %s", got)
	}
}
