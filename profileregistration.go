package embed

import (
	"os"
	"time"

	"github.com/hatchet-dev/hatchet/pkg/config/cli"
	"github.com/hatchet-dev/hatchet/pkg/config/cli/profilestore"
)

// The embedded engine registers itself as a Hatchet CLI profile named
// "embedded" in ~/.hatchet/profiles.yaml when it becomes ready, and removes
// the registration again on graceful shutdown. This lets the hatchet CLI and
// its MCP server discover a running embedded engine through the ordinary
// profile store instead of environment handshakes or port probes.
//
// The store itself is the CLI's own pkg/config/cli/profilestore package, so
// the file location, lock protocol, and on-disk format always agree with the
// CLI. The extra metadata fields written below (embedded, pid, cwd,
// startedat) are ignored by the CLI's lenient reader; the registration itself
// shows up in `hatchet profile list` and is directly usable.
//
// Registration is best effort: any error (including opening the store) is
// logged as a warning by the caller and never fails engine startup. Removal
// is token matched, so an instance that shut down late never deletes a newer
// instance's registration. Last-writer-wins for the single "embedded" name is
// intended.

// embeddedProfileName is the reserved CLI profile name for the embedded engine.
const embeddedProfileName = "embedded"

// embeddedRegistration is the connection info registered for this instance.
type embeddedRegistration struct {
	tenantID    string
	token       string
	apiURL      string
	grpcAddress string
	expiresAt   time.Time
}

// registerEmbeddedProfile upserts the "embedded" profile into the CLI profile
// store, creating ~/.hatchet and the profiles file if absent. All other
// profiles, the default-profile setting, and any unrecognized file content
// (including comments) are preserved. Returns the path written for logging.
func registerEmbeddedProfile(reg embeddedRegistration) (string, error) {
	store, err := profilestore.NewDefaultStore()
	if err != nil {
		return "", err
	}

	// The cwd key is always written, even when empty, so a stale value from a
	// previous registration cannot linger across upserts (UpsertProfile merges
	// into the existing entry rather than replacing it).
	cwd, _ := os.Getwd()

	err = store.UpsertProfile(embeddedProfileName, &cli.Profile{
		TenantId:     reg.tenantID,
		Name:         embeddedProfileName,
		Token:        reg.token,
		ExpiresAt:    reg.expiresAt.UTC(),
		ApiServerURL: reg.apiURL,
		GrpcHostPort: reg.grpcAddress,
		TLSStrategy:  "none",
	}, map[string]any{
		"embedded":  true,
		"pid":       os.Getpid(),
		"cwd":       cwd,
		"startedat": time.Now().UTC(),
	})
	if err != nil {
		return "", err
	}

	return store.Path(), nil
}

// deregisterEmbeddedProfile removes the "embedded" profile from the CLI
// profile store, but only when its token still matches this instance's token:
// a newer instance's registration (last writer wins) is left alone. The
// user's default-profile setting is intentionally never touched. Reports
// whether an entry was removed and the path acted on.
func deregisterEmbeddedProfile(token string) (bool, string, error) {
	store, err := profilestore.NewDefaultStore()
	if err != nil {
		return false, "", err
	}

	removed, err := store.RemoveProfileIfTokenMatches(embeddedProfileName, token)

	return removed, store.Path(), err
}
