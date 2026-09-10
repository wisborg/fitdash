package tilemap

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeKey is what every test here pretends is a secret. It is deliberately
// distinctive so a leak is unambiguous when one is searched for.
const fakeKey = "aabbccdd11223344aabbccdd11223344"

func writeKeyFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestLoadKey_Shapes covers the three file layouts, and the ordering trap
// between them.
//
// A bare key is itself a valid YAML document -- a scalar -- so a loader that
// tried YAML before the plain reading would parse "aabbcc..." as YAML and get
// nothing usable out of it. Only a MAPPING counts as a structured file.
func TestLoadKey_Shapes(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"a bare key", fakeKey},
		{"a bare key with a trailing newline", fakeKey + "\n"},
		{"a bare key with surrounding whitespace", "  " + fakeKey + "\n\n"},
		{"JSON keyed by provider", `{"thunderforest": "` + fakeKey + `"}`},
		{"JSON keyed by \"key\"", `{"key": "` + fakeKey + `"}`},
		{"YAML keyed by provider", "thunderforest: " + fakeKey + "\n"},
		{"YAML keyed by \"key\"", "key: " + fakeKey + "\n"},
		{"YAML with other providers alongside", "mapbox: pk.somethingelse\nthunderforest: " + fakeKey + "\n"},
		{"YAML with a comment", "# my keys\nthunderforest: " + fakeKey + "\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := LoadKey(writeKeyFile(t, "k", c.content), ThunderforestProvider)
			if err != nil {
				t.Fatalf("LoadKey: %v", err)
			}
			if got.reveal() != fakeKey {
				t.Errorf("loaded %q, want the key", got.reveal())
			}
		})
	}
}

// TestLoadKey_ProviderWins pins that a file naming several services hands
// back the right one, which is the entire reason a mapping is supported.
func TestLoadKey_ProviderWins(t *testing.T) {
	path := writeKeyFile(t, "k", "key: the-generic-one\nthunderforest: "+fakeKey+"\n")
	got, err := LoadKey(path, ThunderforestProvider)
	if err != nil {
		t.Fatalf("LoadKey: %v", err)
	}
	if got.reveal() != fakeKey {
		t.Errorf("loaded %q; the provider's own entry must beat the generic one", got.reveal())
	}
}

// TestLoadKey_Failures covers every way this can fail, and asserts the thing
// that matters about all of them: THE FILE'S CONTENTS ARE THE SECRET, so no
// error may quote them. A parse error that echoed the offending line would
// print the key into the terminal and, from there, into whatever the user
// pasted into a bug report.
func TestLoadKey_Failures(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"empty", "", "is empty"},
		{"only whitespace", "   \n\n", "is empty"},
		{"a mapping without this provider", "mapbox: pk.notours\n", "no key for that provider"},
		{"a mapping whose value is not a string", "thunderforest: 12345\n", "no key for that provider"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := writeKeyFile(t, "k", c.content)
			_, err := LoadKey(path, ThunderforestProvider)
			if err == nil {
				t.Fatal("no error")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not explain the problem (%q)", err, c.want)
			}
			if trimmed := strings.TrimSpace(c.content); trimmed != "" && strings.Contains(err.Error(), trimmed) {
				t.Errorf("the error quotes the file's contents, which are the secret: %q", err)
			}
		})
	}

	t.Run("a missing file", func(t *testing.T) {
		_, err := LoadKey(filepath.Join(t.TempDir(), "nope"), ThunderforestProvider)
		if err == nil {
			t.Fatal("no error")
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("error %q does not unwrap to os.ErrNotExist, so a caller cannot tell a missing file from a bad one", err)
		}
	})

	t.Run("the not-found error names the providers that ARE present", func(t *testing.T) {
		path := writeKeyFile(t, "k", "mapbox: pk.notours\nstadia: also-not-ours\n")
		_, err := LoadKey(path, ThunderforestProvider)
		if err == nil {
			t.Fatal("no error")
		}
		// Provider NAMES are not secrets, and listing them is the one thing
		// that makes this failure actionable.
		for _, want := range []string{"mapbox", "stadia"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not name the providers the file does have", err)
			}
		}
		if strings.Contains(err.Error(), "pk.notours") {
			t.Errorf("the error leaks another provider's key: %q", err)
		}
	})
}

// TestKey_RedactsEverywhere is the reason Key is a type rather than a string.
//
// The key travels as a query parameter, so it ends up inside URLs, and URLs
// end up inside errors, logs and JSON output. Each verb below is one that
// somebody writes without thinking, and each must produce nothing useful to
// an attacker reading a terminal over a shoulder or a pasted bug report.
func TestKey_RedactsEverywhere(t *testing.T) {
	k := Key(fakeKey)

	for _, format := range []string{"%s", "%v", "%q", "%#v", "%+v"} {
		if got := fmt.Sprintf(format, k); strings.Contains(got, fakeKey) {
			t.Errorf("fmt %s printed the key: %s", format, got)
		}
	}
	if got := k.String(); got != redacted {
		t.Errorf("String() = %q, want %q", got, redacted)
	}

	// Serialisation matters because fitdash can be asked for JSON output.
	b, err := json.Marshal(struct{ Key Key }{k})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if strings.Contains(string(b), fakeKey) {
		t.Errorf("JSON carried the key: %s", b)
	}

	// And the escape hatch still works, or nothing could use the key at all.
	if k.reveal() != fakeKey {
		t.Error("reveal() does not return the key")
	}
}

// TestCheckKeyFilePermissions warns about a world-readable key and stays
// quiet about a private one. It is a warning rather than a refusal: the file
// is the user's and the permissions may be deliberate.
func TestCheckKeyFilePermissions(t *testing.T) {
	dir := t.TempDir()
	private := filepath.Join(dir, "private")
	if err := os.WriteFile(private, []byte(fakeKey), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckKeyFilePermissions(private); err != nil {
		t.Errorf("a 0600 key file was reported as a problem: %v", err)
	}

	open := filepath.Join(dir, "open")
	if err := os.WriteFile(open, []byte(fakeKey), 0o644); err != nil {
		t.Fatal(err)
	}
	err := CheckKeyFilePermissions(open)
	if err == nil {
		t.Fatal("a world-readable key file drew no warning")
	}
	if strings.Contains(err.Error(), fakeKey) {
		t.Errorf("the warning leaks the key: %v", err)
	}

	// A missing file is LoadKey's problem to report, not this one's.
	if err := CheckKeyFilePermissions(filepath.Join(dir, "nope")); err != nil {
		t.Errorf("a missing file produced a permissions warning: %v", err)
	}
}
