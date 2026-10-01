package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/kensantoso/faws/pkg/awsconfig"
	"github.com/kensantoso/faws/pkg/vend"
)

// vendSelection vends names, which must list non-MFA profiles before MFA
// ones (the caller sorts them), and returns the
// credentials that vended, in input order, plus the profiles it skipped.
//
// One non-MFA profile vends alone first as a canary: an expired SSO login
// fails every profile, and one clear error beats N parallel ones. The rest
// of the non-MFA profiles vend in a pool; MFA profiles vend last, serially,
// because concurrent prompts interleave. A failure skips that profile with a
// warning unless --strict, which aborts on the first one.
func vendSelection(ctx context.Context, c *cobra.Command, o opts, names []string, profileByName map[string]awsconfig.Profile) ([]vend.Creds, []string, error) {
	var plain, mfa []string
	for _, n := range names {
		if profileByName[n].NeedsMFA() {
			mfa = append(mfa, n)
		} else {
			plain = append(plain, n)
		}
	}
	stderr := c.ErrOrStderr()
	// A live block would be overwritten by MFA prompts, so only use it without them.
	live := newProgress(stderr, names, len(mfa) == 0)
	// After Ctrl-C the stop message has moved the cursor, so stop redrawing.
	prog := progressFunc(func(i int, name string, err error) {
		if ctx.Err() == nil {
			live.done(i, name, err)
		}
	})
	timeoutFor := func(p string) time.Duration { return mfaAwareTimeout(o, profileByName[p]) }
	results := make([]vend.Result, len(names))

	fail := func(err error) error {
		if ctx.Err() != nil {
			return errf(130, "%v", err)
		}
		return errf(6, "%v", ssoExpiryHint(err, profileByName))
	}

	if len(plain) > 0 {
		canary := vend.VendPool(ctx, plain[:1], 1, timeoutFor, false, nil)[0]
		prog.done(0, canary.Profile, canary.Err)
		if canary.Err != nil {
			return nil, nil, fail(canary.Err)
		}
		results[0] = canary
		rest := vend.VendPool(ctx, plain[1:], o.parallel, timeoutFor, o.strict, func(r vend.Result) {
			prog.done(1+r.Index, r.Profile, r.Err)
		})
		for _, r := range rest {
			r.Index++
			results[r.Index] = r
		}
	}
	for i, p := range mfa {
		idx := len(plain) + i
		var cr vend.Creds
		var err error
		if ctx.Err() == nil && !(o.strict && firstFailure(results) != nil) {
			cr, err = vend.Vend(ctx, p, c.InOrStdin(), timeoutFor(p), true)
		} else {
			err = fmt.Errorf("vend %q: not attempted", p)
		}
		prog.done(idx, p, err)
		results[idx] = vend.Result{Index: idx, Profile: p, Creds: cr, Err: err}
		// With no plain profile to act as the canary, the first MFA one does.
		if i == 0 && len(plain) == 0 && err != nil {
			return nil, nil, fail(err)
		}
	}

	if ctx.Err() != nil {
		return nil, nil, errf(130, "vend interrupted")
	}
	if o.strict {
		if err := firstFailure(results); err != nil {
			return nil, nil, fail(err)
		}
	}
	var creds []vend.Creds
	var skipped []string
	for _, r := range results {
		if r.Err != nil {
			skipped = append(skipped, r.Profile)
			fmt.Fprintf(stderr, "warning: skipped %s: %s\n", r.Profile, skipReason(ssoExpiryHint(r.Err, profileByName)))
			continue
		}
		creds = append(creds, r.Creds)
	}
	if len(creds) == 0 {
		return nil, nil, errf(6, "no profile vended; %s", skipReason(firstFailure(results)))
	}
	if o.defaultTo != "" && containsName(skipped, o.defaultTo) {
		return nil, nil, errf(6, "--default %s did not vend; see the warning above", o.defaultTo)
	}
	return creds, skipped, nil
}

// progressFunc adapts a func to vendProgress.
type progressFunc func(i int, name string, err error)

func (f progressFunc) done(i int, name string, err error) { f(i, name, err) }

// firstFailure returns the first real error, ignoring profiles the pool
// only skipped because an earlier one failed.
func firstFailure(results []vend.Result) error {
	for _, r := range results {
		if r.Err != nil && !vend.IsStopped(r.Err) {
			return r.Err
		}
	}
	return nil
}

func skippedClause(skipped []string) string {
	if len(skipped) == 0 {
		return ""
	}
	return fmt.Sprintf(" · %d skipped", len(skipped))
}
