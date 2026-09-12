package embed

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hatchet-dev/hatchet/pkg/config/cli"
	"github.com/hatchet-dev/hatchet/pkg/config/cli/profilestore"
)

// These tests cover this repo's registration semantics through the exported
// profilestore API: what the embedded engine writes into the profile entry,
// that registration errors surface (the caller downgrades them to warnings),
// and the token-matched deregistration behavior. The store's own suite in
// github.com/hatchet-dev/hatchet covers the file format details (unquoted
// timestamps, comment preservation, flow-style traps, the lock protocol).

func testRegistration(token string) embeddedRegistration {
	return embeddedRegistration{
		tenantID:    "707d0855-80ab-4e1f-a156-f1c4546cbf52",
		token:       token,
		apiURL:      "http://localhost:28243",
		grpcAddress: "127.0.0.1:50051",
		expiresAt:   time.Now().UTC().Add(90 * 24 * time.Hour),
	}
}

func setTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// readEmbeddedProfile loads the profile store the same way the CLI does and
// returns the "embedded" profile.
func readEmbeddedProfile(t *testing.T) *cli.Profile {
	t.Helper()

	store, err := profilestore.NewDefaultStore()
	if err != nil {
		t.Fatalf("could not open the profile store: %v", err)
	}

	profile, err := store.GetProfile(embeddedProfileName)
	if err != nil {
		t.Fatalf("could not read the embedded profile: %v", err)
	}

	return profile
}

func TestRegisterEmbeddedProfileFreshFile(t *testing.T) {
	home := setTestHome(t)

	path, err := registerEmbeddedProfile(testRegistration("token-fresh"))
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}
	if want := filepath.Join(home, ".hatchet", "profiles.yaml"); path != want {
		t.Fatalf("wrote to %s, want %s", path, want)
	}

	profile := readEmbeddedProfile(t)
	if profile.TenantId != "707d0855-80ab-4e1f-a156-f1c4546cbf52" ||
		profile.Name != "embedded" ||
		profile.Token != "token-fresh" ||
		profile.ApiServerURL != "http://localhost:28243" ||
		profile.GrpcHostPort != "127.0.0.1:50051" ||
		profile.TLSStrategy != "none" {
		t.Errorf("unexpected embedded profile: %+v", profile)
	}
	if profile.ExpiresAt.IsZero() {
		t.Errorf("expiresat did not round-trip through the store")
	}

	// The metadata fields ride along in the same entry (the CLI ignores them).
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("could not read profiles file: %v", err)
	}
	raw := string(data)
	for _, want := range []string{"embedded: true", "pid: ", "cwd: ", "startedat: "} {
		if !strings.Contains(raw, want) {
			t.Errorf("metadata %q missing from profiles file:\n%s", want, raw)
		}
	}
}

func TestRegisterOverwritesPreviousRegistration(t *testing.T) {
	setTestHome(t)

	if _, err := registerEmbeddedProfile(testRegistration("token-old")); err != nil {
		t.Fatalf("first register failed: %v", err)
	}
	if _, err := registerEmbeddedProfile(testRegistration("token-new")); err != nil {
		t.Fatalf("second register failed: %v", err)
	}

	if got := readEmbeddedProfile(t).Token; got != "token-new" {
		t.Errorf("embedded token = %q, want token-new (last writer wins)", got)
	}
}

func TestRegisterPermissionDeniedIsAnError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root; permission checks do not apply")
	}

	home := setTestHome(t)

	dir := filepath.Join(home, ".hatchet")
	if err := os.MkdirAll(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	// The caller treats this as a warning, never a startup failure; here we
	// only assert that it surfaces as an error instead of a panic or a write.
	if _, err := registerEmbeddedProfile(testRegistration("token-denied")); err == nil {
		t.Fatal("expected an error registering into an unwritable directory")
	}
}

func TestDeregisterRemovesMatchingToken(t *testing.T) {
	setTestHome(t)

	if _, err := registerEmbeddedProfile(testRegistration("token-mine")); err != nil {
		t.Fatalf("register failed: %v", err)
	}

	removed, _, err := deregisterEmbeddedProfile("token-mine")
	if err != nil {
		t.Fatalf("deregister failed: %v", err)
	}
	if !removed {
		t.Fatal("expected the registration to be removed")
	}

	store, err := profilestore.NewDefaultStore()
	if err != nil {
		t.Fatalf("could not open the profile store: %v", err)
	}
	if _, err := store.GetProfile(embeddedProfileName); err == nil {
		t.Errorf("embedded profile still present after deregistration")
	}
}

func TestDeregisterKeepsNewerRegistration(t *testing.T) {
	setTestHome(t)

	// A newer instance overwrote the registration; the older instance's
	// shutdown must not delete it.
	if _, err := registerEmbeddedProfile(testRegistration("token-newer")); err != nil {
		t.Fatalf("register failed: %v", err)
	}

	removed, _, err := deregisterEmbeddedProfile("token-older")
	if err != nil {
		t.Fatalf("deregister failed: %v", err)
	}
	if removed {
		t.Fatal("deregistration removed another instance's registration")
	}

	if got := readEmbeddedProfile(t).Token; got != "token-newer" {
		t.Errorf("embedded token = %q, want token-newer", got)
	}
}

func TestDeregisterWithoutFileIsANoOp(t *testing.T) {
	setTestHome(t)

	removed, _, err := deregisterEmbeddedProfile("token-any")
	if err != nil {
		t.Fatalf("deregister failed: %v", err)
	}
	if removed {
		t.Fatal("nothing to remove, but removed was reported")
	}
}
