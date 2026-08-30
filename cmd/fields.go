package cmd

import (
	"fmt"
	"strings"
	"time"
)

// splitFields splits raw on unescaped commas for flag's own comma-separated
// key=value grammar. \, is a literal comma and \\ a literal backslash; no
// other escape is recognised, so a stray backslash is refused rather than
// silently swallowed or passed through with a meaning nobody asked for.
//
// Pulled out of --highlight's own parsing (see cmd/highlight.go) so a second
// flag using the same grammar -- --label -- shares the lexer rather than
// re-implementing the same two escapes with its own chance to drift. flag is
// threaded through purely so an error still names what the user typed
// (--highlight or --label), not because the two escapes differ between them.
func splitFields(raw, flag string) ([]string, error) {
	var fields []string
	var cur strings.Builder
	escaped := false
	for _, r := range raw {
		if escaped {
			switch r {
			case ',', '\\':
				cur.WriteRune(r)
			default:
				return nil, fmt.Errorf("render: %s %q: unknown escape \\%c", flag, raw, r)
			}
			escaped = false
			continue
		}
		switch r {
		case '\\':
			escaped = true
		case ',':
			fields = append(fields, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if escaped {
		return nil, fmt.Errorf("render: %s %q ends with a trailing backslash", flag, raw)
	}
	fields = append(fields, cur.String())
	return fields, nil
}

// parseFields splits raw via splitFields and cuts each field on its FIRST "="
// into a key/value map -- so a value may itself contain "=" freely, only a key
// never can -- refusing a field with no "=" at all and a repeated key rather
// than silently preferring one of two contradictory values for the same
// field, which is the same failure class as silently substituting a default.
//
// Only the split, the cut and the repeated-key refusal live here. Each
// caller's own switch over the returned map does its per-key validation with
// its own error wording -- see parseHighlight -- because that is where the
// good messages live and a shared validator would have to speak for fields it
// does not understand.
func parseFields(raw, flag string) (map[string]string, error) {
	fields, err := splitFields(raw, flag)
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(fields))
	for _, field := range fields {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			return nil, fmt.Errorf("render: %s %q: field %q has no \"=\"", flag, raw, field)
		}
		if _, seen := m[key]; seen {
			return nil, fmt.Errorf("render: %s %q: %q is repeated", flag, raw, key)
		}
		m[key] = value
	}
	return m, nil
}

// parseDurationField parses value as a Go duration for key within flag's raw
// value, wording the error the same way every duration field in this CLI
// does. Removes five copies of this message (--highlight's from, to and
// video; --label's at and video).
func parseDurationField(flag, raw, key, value string) (time.Duration, error) {
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("render: %s %q: %s %q is not a duration: %w", flag, raw, key, value, err)
	}
	return d, nil
}
