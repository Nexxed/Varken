// Package config reads varken.ini and the VRKN_* environment variables with the same rules as the Python
// version, so an existing configuration keeps working unchanged.
package config

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	py "github.com/Nexxed/Varken/internal/pycompat"
)

// INI is a parsed file with Python configparser semantics (interpolation off): "=" or ":" delimiters, full-line
// comments only (# and ; may appear inside values), lower-cased option names, case-sensitive section names,
// indented continuation lines joined with newlines, and a DEFAULT section that every section inherits.
type INI struct {
	sections map[string]map[string]string
	order    []string
	defaults map[string]string
	// Warnings lists lines that could not be parsed and duplicates (Python stopped with an error; these are
	// reported and the rest of the file is used).
	Warnings []string
}

var (
	sectionRE = regexp.MustCompile(`^\[(.+)\]`)
	optionRE  = regexp.MustCompile(`^(.*?)\s*([=:])\s*(.*)$`)
)

const defaultSection = "DEFAULT"

// ParseINI parses the text of an ini file.
func ParseINI(text string) *INI {
	ini := &INI{sections: map[string]map[string]string{}, defaults: map[string]string{}}
	text = strings.TrimPrefix(text, "\ufeff")
	text = strings.ReplaceAll(text, "\r\n", "\n")

	type value struct{ lines []string }
	var (
		cur         map[string]*value
		curName     string
		optName     string
		indentLevel = 0
		raw         = map[string]map[string]*value{}
		seenSection = map[string]bool{}
		seenOption  = map[string]bool{}
	)
	defaults := map[string]*value{}
	section := func(name string) map[string]*value {
		if name == defaultSection {
			return defaults
		}
		if raw[name] == nil {
			raw[name] = map[string]*value{}
			ini.order = append(ini.order, name)
		}
		return raw[name]
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lineno := i + 1
		trimmed := strings.TrimFunc(line, isSpace)
		isComment := strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";")
		if trimmed == "" || isComment {
			if !isComment && cur != nil && optName != "" && cur[optName] != nil {
				cur[optName].lines = append(cur[optName].lines, "")
			}
			continue
		}
		indent := utf8.RuneCountInString(line[:strings.IndexFunc(line, func(r rune) bool { return !isSpace(r) })])
		if cur != nil && optName != "" && indent > indentLevel {
			cur[optName].lines = append(cur[optName].lines, trimmed)
			continue
		}
		indentLevel = indent
		if m := sectionRE.FindStringSubmatch(trimmed); m != nil {
			name := m[1]
			if seenSection[name] {
				ini.Warnings = append(ini.Warnings, fmt.Sprintf("line %d: section [%s] appears more than once; "+
					"its options are merged", lineno, name))
			}
			seenSection[name] = true
			cur, curName, optName = section(name), name, ""
			continue
		}
		if cur == nil {
			ini.Warnings = append(ini.Warnings, fmt.Sprintf("line %d: option outside of any section ignored: %s",
				lineno, trimmed))
			continue
		}
		m := optionRE.FindStringSubmatch(trimmed)
		if m == nil || strings.TrimRight(m[1], " \t") == "" {
			ini.Warnings = append(ini.Warnings, fmt.Sprintf("line %d: cannot parse %q", lineno, trimmed))
			continue
		}
		name := strings.ToLower(strings.TrimRightFunc(m[1], isSpace))
		key := curName + "\x00" + name
		if seenOption[key] {
			ini.Warnings = append(ini.Warnings, fmt.Sprintf("line %d: option %s appears more than once in [%s]; "+
				"the last value is used", lineno, name, curName))
		}
		seenOption[key] = true
		optName = name
		cur[name] = &value{lines: []string{strings.TrimFunc(m[3], isSpace)}}
	}
	join := func(v *value) string {
		return strings.TrimRightFunc(strings.Join(v.lines, "\n"), isSpace)
	}
	for k, v := range defaults {
		ini.defaults[k] = join(v)
	}
	for name, opts := range raw {
		out := map[string]string{}
		for k, v := range opts {
			out[k] = join(v)
		}
		ini.sections[name] = out
	}
	return ini
}

func isSpace(r rune) bool { return py.IsSpace(r) }

// HasSection reports whether the section exists (DEFAULT does not count, as in Python).
func (ini *INI) HasSection(name string) bool {
	_, ok := ini.sections[name]
	return ok
}

// Sections lists the sections in file order.
func (ini *INI) Sections() []string { return append([]string(nil), ini.order...) }

// Get returns an option, falling back to DEFAULT. ok is false when the section or the option is missing.
func (ini *INI) Get(section, option string) (string, bool) {
	opts, ok := ini.sections[section]
	if !ok {
		return "", false
	}
	option = strings.ToLower(option)
	if v, ok := opts[option]; ok {
		return v, true
	}
	v, ok := ini.defaults[option]
	return v, ok
}

// Options returns a section's options, including those inherited from DEFAULT.
func (ini *INI) Options(section string) map[string]string {
	out := map[string]string{}
	for k, v := range ini.defaults {
		out[k] = v
	}
	for k, v := range ini.sections[section] {
		out[k] = v
	}
	return out
}
