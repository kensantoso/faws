// Package filter selects AWS profiles by substring over the profile name and
// every value in the section. It knows nothing about what the keys mean.
package filter

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/kensantoso/faws/pkg/awsconfig"
)

// SecretKeys are never matched against: matching on secret material is
// useless and a small oracle for probing a secret's contents.
var SecretKeys = map[string]bool{
	"aws_access_key_id":     true,
	"aws_secret_access_key": true,
	"aws_session_token":     true,
}

// Filter is the one selection concept. Terms within a group AND; groups OR;
// Exclude is subtracted last and always wins.
type Filter struct {
	Groups  [][]string
	Exclude []string
}

// Match is a profile that passed the filter, plus the keys that satisfied it.
type Match struct {
	Profile awsconfig.Profile
	Keys    []string
}

// New builds a Filter. Each --all value is one AND group. Each --any value
// contributes one group per term, which is exactly repeated --all. Terms are
// lowercased here so matching only has to fold the profile's own values.
func New(all, any, exclude []string) (Filter, error) {
	var f Filter
	for _, raw := range all {
		terms, err := splitTerms(raw)
		if err != nil {
			return Filter{}, err
		}
		f.Groups = append(f.Groups, terms)
	}
	for _, raw := range any {
		terms, err := splitTerms(raw)
		if err != nil {
			return Filter{}, err
		}
		for _, t := range terms {
			f.Groups = append(f.Groups, []string{t})
		}
	}
	for _, raw := range exclude {
		terms, err := splitTerms(raw)
		if err != nil {
			return Filter{}, err
		}
		f.Exclude = append(f.Exclude, terms...)
	}
	return f, nil
}

// splitTerms splits a comma-separated flag value into lowercased terms. An
// empty term is an error rather than one that silently matches everything.
func splitTerms(raw string) ([]string, error) {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		t := strings.ToLower(strings.TrimSpace(part))
		if t == "" {
			return nil, fmt.Errorf("empty term in filter %q", raw)
		}
		if strings.ContainsFunc(t, unicode.IsSpace) {
			return nil, fmt.Errorf("term %q contains whitespace; terms cannot contain spaces — use comma-separated terms instead (e.g. --all my,work)", t)
		}
		out = append(out, t)
	}
	return out, nil
}

// Apply returns the profiles the filter selects, in input order.
func (f Filter) Apply(profiles []awsconfig.Profile) []Match {
	out := make([]Match, 0, len(profiles))
	for _, p := range profiles {
		if excluded(p, f.Exclude) {
			continue
		}
		if len(f.Groups) == 0 {
			out = append(out, Match{Profile: p})
			continue
		}
		if keys, ok := matchAnyGroup(p, f.Groups); ok {
			out = append(out, Match{Profile: p, Keys: keys})
		}
	}
	return out
}

// matchAnyGroup returns the keys that satisfied the first group all of whose
// terms match.
func matchAnyGroup(p awsconfig.Profile, groups [][]string) ([]string, bool) {
	for _, group := range groups {
		var keys []string
		ok := true
		for _, term := range group {
			key, found := matchTerm(p, term)
			if !found {
				ok = false
				break
			}
			if !contains(keys, key) {
				keys = append(keys, key)
			}
		}
		if ok {
			return keys, true
		}
	}
	return nil, false
}

func excluded(p awsconfig.Profile, terms []string) bool {
	for _, t := range terms {
		if _, found := matchTerm(p, t); found {
			return true
		}
	}
	return false
}

// matchTerm reports whether term appears in the profile name or any
// non-secret value — including a nested setting's value (item 3), reported
// under "parent.child", e.g. "s3.max_concurrent_requests" — and names the
// key it matched ("name" for the section name). Keys are checked in written
// order so the reported key is stable.
func matchTerm(p awsconfig.Profile, term string) (string, bool) {
	if strings.Contains(strings.ToLower(p.Name), term) {
		return "name", true
	}
	for _, k := range p.Order {
		if SecretKeys[k] {
			continue
		}
		if strings.Contains(strings.ToLower(p.Keys[k]), term) {
			return k, true
		}
		if ns, ok := p.Nested[k]; ok {
			for _, ck := range ns.Order {
				if strings.Contains(strings.ToLower(ns.Keys[ck]), term) {
					return k + "." + ck, true
				}
			}
		}
	}
	return "", false
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
