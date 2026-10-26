package cmd

import (
	"github.com/spf13/cobra"

	cs "github.com/wisborg/cobrakit/secrets"
	"github.com/wisborg/secrets"

	"github.com/wisborg/fitdash/internal/tilemap"
)

// addSecrets puts the map service key among the secrets "fitdash secrets"
// lists and sets, beside the Strava ones cobrakit's strava.Add puts there.
// A file --basemap-key-file names is a render's alone, so the list says
// where else the key is looked for.
func addSecrets(root *cobra.Command) {
	cs.Add(root, func(*cobra.Command) ([]secrets.Secret, error) {
		return []secrets.Secret{tilemap.KeySecret("")}, nil
	})
}
