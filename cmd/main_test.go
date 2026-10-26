package cmd

import (
	"os"
	"testing"

	"github.com/wisborg/secrets"
)

// The tests keep secrets in files, as on a system with no keychain, unless
// one says otherwise; and the keychain they would reach is one in memory,
// never the user's own.
func TestMain(m *testing.M) {
	secrets.MockKeychain(nil)
	os.Setenv(secrets.EnvStore, "file")
	os.Exit(m.Run())
}
