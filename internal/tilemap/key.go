package tilemap

import (
	"fmt"
	"os"

	"github.com/wisborg/secrets"
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

// KeyName is the secret a Thunderforest key is known by: in the system's
// keychain, the item key of the service wisborg/thunderforest.
const KeyName = ThunderforestProvider + "/key"

// EnvKey is the variable a Thunderforest key may be given in.
const EnvKey = "THUNDERFOREST_API_KEY"

// KeySecret is the Thunderforest key, with where it is looked for: the key
// file at path when one is named -- a file asked for is the one wanted --
// then $THUNDERFOREST_API_KEY, then the system's keychain.
func KeySecret(path string) secrets.Secret {
	var chain secrets.Chain
	if path != "" {
		chain = append(chain, keyFile(path, ThunderforestProvider))
	}
	chain = append(chain, secrets.Env{KeyName: EnvKey}, secrets.Keychain{})
	return secrets.Secret{Name: KeyName, About: "your Thunderforest API key, for --basemap; or in the file --basemap-key-file names", Sources: chain}
}

// FindKey is the Thunderforest key from wherever KeySecret(path) looks,
// and where it was found.
func FindKey(path string) (Key, string, error) {
	s := KeySecret(path)
	v, from, err := s.Get()
	if err != nil {
		return "", "", err
	}
	return Key(v), from.Where(s.Name), nil
}

// keyFile is a key file for provider: the bare key, or a JSON or YAML
// mapping of provider name to key, looked up by provider and then under
// "key", so a file holding one key needs no provider name and a file
// holding several keeps them apart.
func keyFile(path, provider string) secrets.MappingFile {
	name := provider + "/key"
	return secrets.MappingFile{Path: path, Keys: map[string][]string{name: {provider, "key"}}, Bare: name}
}

// LoadKey reads provider's API key out of the file at path.
//
// Three shapes are accepted: a JSON object mapping provider names to keys,
// a YAML mapping of the same, and anything else, which is the key itself,
// trimmed. A bare key is itself a valid YAML document -- a scalar -- so
// only a MAPPING counts as a structured file.
//
// NOTHING READ FROM THE FILE APPEARS IN ANY ERROR THIS RETURNS. The file's
// contents are the secret, so a parse error that quoted the offending text
// would be the leak the Key type exists to prevent -- which is also why a
// malformed JSON or YAML document is not reported as such but simply falls
// through to being read as a plain key. A mapping without the provider is
// reported with the names it does have, which are not secrets.
func LoadKey(path, provider string) (Key, error) {
	v, err := keyFile(path, provider).Get(provider + "/key")
	if err != nil {
		return "", fmt.Errorf("the %s key file: %w", provider, err)
	}
	return Key(v), nil
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
