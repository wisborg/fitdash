package tilemap

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	yaml "go.yaml.in/yaml/v3"
)

// Key is a map service's API key.
//
// It is a distinct type for one reason: every way of printing it is
// overridden to redact. The key travels to the service as a QUERY PARAMETER,
// so it ends up inside a URL, and a URL ends up inside errors, logs and, in a
// program that writes JSON, inside output. A plain string would leak through
// any of those the first time somebody wrote %v without thinking about it.
//
// String, GoString and MarshalText cover the formatting verbs that consult
// them (fmt uses Stringer for %v, %s and %q alike) and the encoding path.
// Reading the actual value takes the unexported reveal, which only this
// package can call -- and this package builds the URL, so that is the only
// place it is needed.
type Key string

// String redacts. So do the other two.
func (Key) String() string { return redacted }

// GoString redacts %#v, which would otherwise print the underlying string.
func (Key) GoString() string { return `"` + redacted + `"` }

// MarshalText redacts the key anywhere it is serialised -- which matters
// because fitdash can be asked for its output as JSON or YAML.
func (Key) MarshalText() ([]byte, error) { return []byte(redacted), nil }

const redacted = "[redacted]"

// reveal is the only way to the underlying value, and is deliberately not
// exported.
func (k Key) reveal() string { return string(k) }

// Empty reports whether there is no key at all.
func (k Key) Empty() bool { return k == "" }

// ErrNoKeyForProvider is returned when a key file is a mapping that does not
// name the provider being asked for.
var ErrNoKeyForProvider = errors.New("no key for that provider in the key file")

// LoadKey reads provider's API key out of the file at path.
//
// Three shapes are accepted, tried in this order:
//
//  1. A JSON object mapping provider names to keys.
//  2. A YAML mapping of the same.
//  3. Anything else: the whole file, trimmed, IS the key.
//
// The order is not arbitrary and cannot be relaxed. A bare key is itself
// valid YAML -- a scalar -- so trying YAML before the plain case would parse
// "abc123" as a YAML document and get nothing useful out of it. Only a
// MAPPING counts as a key file; anything a YAML parser reduces to a scalar
// falls through to the plain reading.
//
// A mapping is looked up by provider name first and then under "key", so a
// file holding one key needs no provider name and a file holding several
// keeps them apart. That is the whole schema: no nesting, no other fields.
//
// NOTHING READ FROM THE FILE APPEARS IN ANY ERROR THIS RETURNS. The file's
// contents are the secret, so a parse error that quoted the offending text
// would be the leak the Key type exists to prevent -- which is also why a
// malformed JSON or YAML document is not reported as such but simply falls
// through to being read as a plain key, and fails, if at all, as an empty or
// unusable key later.
func LoadKey(path, provider string) (Key, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		// os.ReadFile's error names the path and the reason and never the
		// contents, so it is safe to pass through.
		return "", fmt.Errorf("reading the %s key file: %w", provider, err)
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return "", fmt.Errorf("the %s key file %s is empty", provider, path)
	}

	if m, ok := parseMapping(text); ok {
		for _, name := range []string{provider, "key"} {
			if v := strings.TrimSpace(m[name]); v != "" {
				return Key(v), nil
			}
		}
		// The map's KEYS are provider names, not secrets, so listing them is
		// safe and is the one thing that makes this failure actionable.
		return "", fmt.Errorf("%w: %s names %s", ErrNoKeyForProvider, path, quotedNames(m))
	}
	return Key(text), nil
}

// parseMapping reports whether text is a JSON object or a YAML mapping of
// strings to strings, and returns it if so.
//
// Values that are not strings are dropped rather than rejected: a key file
// may reasonably carry a number or a nested block for something else, and
// refusing the whole file over a field this package does not want would be
// unhelpful. A provider whose value is not a string simply has no key here.
func parseMapping(text string) (map[string]string, bool) {
	if strings.HasPrefix(text, "{") {
		var obj map[string]any
		if err := json.Unmarshal([]byte(text), &obj); err != nil {
			return nil, false
		}
		return stringValues(obj), true
	}
	var node map[string]any
	if err := yaml.Unmarshal([]byte(text), &node); err != nil {
		// Not a mapping -- a bare key is a valid YAML scalar and lands here.
		return nil, false
	}
	if len(node) == 0 {
		return nil, false
	}
	return stringValues(node), true
}

func stringValues(in map[string]any) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

func quotedNames(m map[string]string) string {
	if len(m) == 0 {
		return "no providers"
	}
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, strconv.Quote(k))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// CheckKeyFilePermissions reports a non-fatal problem with how the key file is
// protected, or nil when it looks fine.
//
// A key readable by everyone on the machine is worth saying something about
// once, and worth saying it as a WARNING rather than a refusal: the file is
// the user's, the permissions may be deliberate, and a render that stopped
// over them would be this program deciding something that is not its
// decision. It is separate from LoadKey so the caller chooses whether to
// print it.
func CheckKeyFilePermissions(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return nil // LoadKey will report this properly; nothing to add here.
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return fmt.Errorf("%s is readable by other users (mode %#o); consider chmod 600", path, mode)
	}
	return nil
}
