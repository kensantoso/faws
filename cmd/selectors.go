package cmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"

	"github.com/kensantoso/faws/pkg/awsconfig"
	"github.com/kensantoso/faws/pkg/filter"
)

// selectorNameRE keeps names safe as a single shell word and a store key.
var selectorNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func validSelectorName(name string) error {
	if !selectorNameRE.MatchString(name) {
		return errf(3, "selector name %q is invalid; use letters, digits, '.', '_' or '-'", name)
	}
	return nil
}

// selectorsPath follows XDG on every OS, so the store is easy to find and edit.
func selectorsPath() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "faws", "selectors"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "faws", "selectors"), nil
}

// selectorStore keeps the raw lines so a rewrite preserves comments and order.
type selectorStore struct {
	path  string
	lines []string
}

type selector struct {
	name string
	line string // invocation syntax, as parseInvocationLine reads it
}

func loadSelectors() (*selectorStore, error) {
	path, err := selectorsPath()
	if err != nil {
		return nil, errf(2, "locate selector store: %v", err)
	}
	s := &selectorStore{path: path}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, errf(2, "read selector store: %v", err)
	}
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		s.lines = append(s.lines, sc.Text())
	}
	return s, nil
}

// parseSelectorLine returns ok=false for blank and comment lines.
func parseSelectorLine(raw string) (selector, bool) {
	line := strings.TrimSpace(raw)
	if line == "" || strings.HasPrefix(line, "#") {
		return selector{}, false
	}
	name, rest, found := strings.Cut(line, "=")
	if !found {
		return selector{}, false
	}
	return selector{name: strings.TrimSpace(name), line: strings.TrimSpace(rest)}, true
}

func (s *selectorStore) all() []selector {
	var out []selector
	for _, raw := range s.lines {
		if sel, ok := parseSelectorLine(raw); ok {
			out = append(out, sel)
		}
	}
	return out
}

func (s *selectorStore) get(name string) (selector, bool) {
	for _, sel := range s.all() {
		if sel.name == name {
			return sel, true
		}
	}
	return selector{}, false
}

// set replaces name's line in place, or appends it.
func (s *selectorStore) set(name, line string) {
	entry := name + " = " + line
	for i, raw := range s.lines {
		if sel, ok := parseSelectorLine(raw); ok && sel.name == name {
			s.lines[i] = entry
			return
		}
	}
	s.lines = append(s.lines, entry)
}

func (s *selectorStore) remove(name string) bool {
	for i, raw := range s.lines {
		if sel, ok := parseSelectorLine(raw); ok && sel.name == name {
			s.lines = append(s.lines[:i], s.lines[i+1:]...)
			return true
		}
	}
	return false
}

// save writes atomically, like output.WriteFile, so a crash never truncates the store.
func (s *selectorStore) save() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".selectors-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	body := strings.Join(s.lines, "\n")
	if body != "" {
		body += "\n"
	}
	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

func (s *selectorStore) names() string {
	var names []string
	for _, sel := range s.all() {
		names = append(names, "@"+sel.name)
	}
	if len(names) == 0 {
		return "none saved"
	}
	return strings.Join(names, ", ")
}

// resolveSelector loads @name as the base opts for this invocation.
func resolveSelector(name string) (opts, error) {
	if err := validSelectorName(name); err != nil {
		return opts{}, err
	}
	store, err := loadSelectors()
	if err != nil {
		return opts{}, err
	}
	sel, ok := store.get(name)
	if !ok {
		return opts{}, errf(3, "no saved selector @%s (have: %s)", name, store.names())
	}
	base, err := parseInvocationLine(sel.line)
	if err != nil {
		return opts{}, errf(3, "selector @%s in %s: %v", name, store.path, err)
	}
	return base, nil
}

// mergeSelector layers the flags typed on this invocation over a saved
// selector: list flags add to it, scalar flags replace it only when typed.
func mergeSelector(base, cli opts, changed func(string) bool) opts {
	m := cli
	m.all = append(append([]string{}, base.all...), cli.all...)
	m.any = append(append([]string{}, base.any...), cli.any...)
	m.exclude = append(append([]string{}, base.exclude...), cli.exclude...)
	m.set = append(append([]string{}, base.set...), cli.set...)
	m.noCopy = base.noCopy || cli.noCopy
	pick := func(flag, saved, typed string) string {
		if changed(flag) {
			return typed
		}
		return saved
	}
	m.format = pick("format", base.format, cli.format)
	m.region = pick("region", base.region, cli.region)
	m.outputFmt = pick("output", base.outputFmt, cli.outputFmt)
	m.defaultTo = pick("default", base.defaultTo, cli.defaultTo)
	if m.format == "" {
		m.format = "config"
	}
	return m
}

// widens reports whether extra --all/--any groups add profiles to a selector
// that already had groups; on a selector with none they narrow instead.
func widens(base, cli opts) bool {
	return len(base.all)+len(base.any) > 0 && len(cli.all)+len(cli.any) > 0
}

// splitSelectorLabel strips the "@name = " label that a selector-based vend
// writes in front of the recorded flags.
func splitSelectorLabel(line string) (label, flags string) {
	if strings.HasPrefix(line, "@") {
		if name, rest, ok := strings.Cut(line, " = "); ok {
			return name, rest
		}
	}
	return "", line
}

// listSelectors prints each saved selector with its current match count.
func listSelectors(w io.Writer) error {
	store, err := loadSelectors()
	if err != nil {
		return err
	}
	sels := store.all()
	if len(sels) == 0 {
		_, err := fmt.Fprintf(w, "no saved selectors in %s; save one with --save NAME\n", store.path)
		return err
	}
	profiles, err := awsconfig.Load()
	if err != nil {
		return errf(2, "read AWS config: %v", err)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, sel := range sels {
		fmt.Fprintf(tw, "@%s\t%s\t%s\n", sel.name, selectorCount(sel, profiles), sel.line)
	}
	return tw.Flush()
}

func selectorCount(sel selector, profiles []awsconfig.Profile) string {
	o, err := parseInvocationLine(sel.line)
	if err != nil {
		return "invalid"
	}
	f, err := filter.New(o.all, o.any, o.exclude)
	if err != nil {
		return "invalid"
	}
	n := len(f.Apply(profiles))
	if n == 1 {
		return "1 profile"
	}
	return fmt.Sprintf("%d profiles", n)
}

// saveSelector records the effective invocation under name.
func saveSelector(w io.Writer, name string, o opts, force bool) error {
	store, err := loadSelectors()
	if err != nil {
		return err
	}
	line := invocationLine(o)
	if old, ok := store.get(name); ok && old.line != line && !force {
		return errf(3, "@%s already exists (%s); rerun with --force to replace it with: %s", name, old.line, line)
	}
	store.set(name, line)
	if err := store.save(); err != nil {
		return errf(2, "write selector store: %v", err)
	}
	_, err = fmt.Fprintf(w, "saved @%s = %s\n", name, line)
	return err
}

func forgetSelector(w io.Writer, name string) error {
	store, err := loadSelectors()
	if err != nil {
		return err
	}
	if !store.remove(name) {
		return errf(3, "no saved selector @%s (have: %s)", name, store.names())
	}
	if err := store.save(); err != nil {
		return errf(2, "write selector store: %v", err)
	}
	_, err = fmt.Fprintf(w, "forgot @%s\n", name)
	return err
}
