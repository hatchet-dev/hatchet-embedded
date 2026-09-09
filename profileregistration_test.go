package embed

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

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

func readProfilesFile(t *testing.T, home string) (string, map[string]any) {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(home, ".hatchet", "profiles.yaml"))
	if err != nil {
		t.Fatalf("could not read profiles file: %v", err)
	}

	var parsed map[string]any
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("written profiles file does not parse: %v", err)
	}

	return string(data), parsed
}

func profileEntry(t *testing.T, parsed map[string]any, name string) map[string]any {
	t.Helper()

	profiles, ok := parsed["profiles"].(map[string]any)
	if !ok {
		t.Fatalf("profiles key missing or not a mapping: %#v", parsed["profiles"])
	}
	entry, ok := profiles[name].(map[string]any)
	if !ok {
		t.Fatalf("profile %q missing or not a mapping: %#v", name, profiles[name])
	}

	return entry
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

	raw, parsed := readProfilesFile(t, home)
	entry := profileEntry(t, parsed, "embedded")

	for key, want := range map[string]any{
		"tenantid":     "707d0855-80ab-4e1f-a156-f1c4546cbf52",
		"name":         "embedded",
		"token":        "token-fresh",
		"apiserverurl": "http://localhost:28243",
		"grpchostport": "127.0.0.1:50051",
		"tlsstrategy":  "none",
		"embedded":     true,
	} {
		if entry[key] != want {
			t.Errorf("entry[%q] = %#v, want %#v", key, entry[key], want)
		}
	}
	if _, ok := entry["pid"].(int); !ok {
		t.Errorf("entry[pid] = %#v, want an int", entry["pid"])
	}

	// The CLI decodes expiresat into a time.Time; a quoted string there would
	// break its unmarshalling, so the timestamp must be written unquoted.
	if strings.Contains(raw, `expiresat: "`) || strings.Contains(raw, "expiresat: '") {
		t.Errorf("expiresat was written quoted:\n%s", raw)
	}
	if _, ok := entry["expiresat"].(time.Time); !ok {
		t.Errorf("expiresat did not parse as a yaml timestamp: %#v", entry["expiresat"])
	}
}

// TestRegisteredProfileReadableByViper anchors interop with the CLI: the CLI
// loads the profiles file via viper (lenient unmarshal into a struct with
// time.Time fields) and per-key lookups, so both must work on our output.
func TestRegisteredProfileReadableByViper(t *testing.T) {
	home := setTestHome(t)

	if _, err := registerEmbeddedProfile(testRegistration("token-viper")); err != nil {
		t.Fatalf("register failed: %v", err)
	}

	v := viper.New()
	v.SetConfigFile(filepath.Join(home, ".hatchet", "profiles.yaml"))
	v.SetConfigType("yaml")
	if err := v.ReadInConfig(); err != nil {
		t.Fatalf("viper could not read the profiles file: %v", err)
	}

	if got := v.GetString("profiles.embedded.token"); got != "token-viper" {
		t.Errorf("viper token = %q, want token-viper", got)
	}
	if got := v.GetTime("profiles.embedded.expiresat"); got.IsZero() {
		t.Errorf("viper expiresat is zero")
	}

	// Mirrors pkg/config/cli.ProfileFile in the CLI: unknown metadata fields
	// must not break the struct unmarshal.
	type profile struct {
		TenantId     string    `mapstructure:"tenantId"`
		Name         string    `mapstructure:"name"`
		Token        string    `mapstructure:"token"`
		ExpiresAt    time.Time `mapstructure:"expiresAt"`
		ApiServerURL string    `mapstructure:"apiServerURL"`
		GrpcHostPort string    `mapstructure:"grpcHostPort"`
		TLSStrategy  string    `mapstructure:"tlsStrategy"`
	}
	var file struct {
		Profiles map[string]profile `mapstructure:"profiles"`
	}
	if err := v.Unmarshal(&file); err != nil {
		t.Fatalf("viper struct unmarshal failed (the released CLI would fatal on this): %v", err)
	}

	got := file.Profiles["embedded"]
	if got.Token != "token-viper" || got.TLSStrategy != "none" || got.ExpiresAt.IsZero() {
		t.Errorf("unmarshalled profile = %+v", got)
	}
}

func TestRegisterPreservesExistingContent(t *testing.T) {
	home := setTestHome(t)

	existing := `# keep this comment
defaultprofile: prod
profiles:
    prod:
        apiserverurl: https://prod.example.com
        expiresat: 2027-01-02T03:04:05Z
        grpchostport: prod.example.com:443
        name: prod
        tenantid: 11111111-2222-3333-4444-555555555555
        tlsstrategy: tls
        token: prod-token
`
	dir := filepath.Join(home, ".hatchet")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "profiles.yaml"), []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := registerEmbeddedProfile(testRegistration("token-merge")); err != nil {
		t.Fatalf("register failed: %v", err)
	}

	raw, parsed := readProfilesFile(t, home)

	if parsed["defaultprofile"] != "prod" {
		t.Errorf("defaultprofile = %#v, want prod", parsed["defaultprofile"])
	}
	if !strings.Contains(raw, "# keep this comment") {
		t.Errorf("comment was not preserved:\n%s", raw)
	}

	prod := profileEntry(t, parsed, "prod")
	for key, want := range map[string]any{
		"apiserverurl": "https://prod.example.com",
		"grpchostport": "prod.example.com:443",
		"name":         "prod",
		"tenantid":     "11111111-2222-3333-4444-555555555555",
		"tlsstrategy":  "tls",
		"token":        "prod-token",
	} {
		if prod[key] != want {
			t.Errorf("prod[%q] = %#v, want %#v", key, prod[key], want)
		}
	}

	embedded := profileEntry(t, parsed, "embedded")
	if embedded["token"] != "token-merge" {
		t.Errorf("embedded token = %#v", embedded["token"])
	}
}

func TestRegisterOverwritesPreviousRegistration(t *testing.T) {
	home := setTestHome(t)

	if _, err := registerEmbeddedProfile(testRegistration("token-old")); err != nil {
		t.Fatalf("first register failed: %v", err)
	}
	if _, err := registerEmbeddedProfile(testRegistration("token-new")); err != nil {
		t.Fatalf("second register failed: %v", err)
	}

	_, parsed := readProfilesFile(t, home)
	entry := profileEntry(t, parsed, "embedded")
	if entry["token"] != "token-new" {
		t.Errorf("embedded token = %#v, want token-new (last writer wins)", entry["token"])
	}
}

// TestReRegistrationAfterDeregistrationStaysCLIReadable guards against a flow
// style trap: a deregistration that empties the store leaves "profiles: {}"
// behind, and a later registration merged into that flow mapping would be
// emitted in flow style with quoted timestamps, which the CLI cannot decode
// into its time.Time fields.
func TestReRegistrationAfterDeregistrationStaysCLIReadable(t *testing.T) {
	home := setTestHome(t)

	if _, err := registerEmbeddedProfile(testRegistration("token-first")); err != nil {
		t.Fatalf("register failed: %v", err)
	}
	if _, _, err := deregisterEmbeddedProfile("token-first"); err != nil {
		t.Fatalf("deregister failed: %v", err)
	}
	if _, err := registerEmbeddedProfile(testRegistration("token-second")); err != nil {
		t.Fatalf("re-register failed: %v", err)
	}

	raw, _ := readProfilesFile(t, home)
	if strings.Contains(raw, "{") {
		t.Errorf("profiles file was written in flow style:\n%s", raw)
	}

	v := viper.New()
	v.SetConfigFile(filepath.Join(home, ".hatchet", "profiles.yaml"))
	v.SetConfigType("yaml")
	if err := v.ReadInConfig(); err != nil {
		t.Fatalf("viper could not read the profiles file: %v", err)
	}
	var file struct {
		Profiles map[string]struct {
			ExpiresAt time.Time `mapstructure:"expiresAt"`
			Token     string    `mapstructure:"token"`
		} `mapstructure:"profiles"`
	}
	if err := v.Unmarshal(&file); err != nil {
		t.Fatalf("viper struct unmarshal failed (the released CLI would fatal on this): %v", err)
	}
	if file.Profiles["embedded"].Token != "token-second" || file.Profiles["embedded"].ExpiresAt.IsZero() {
		t.Errorf("unexpected re-registered profile: %+v", file.Profiles["embedded"])
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
	home := setTestHome(t)

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

	_, parsed := readProfilesFile(t, home)
	if profiles, ok := parsed["profiles"].(map[string]any); ok {
		if _, still := profiles["embedded"]; still {
			t.Errorf("embedded profile still present after deregistration")
		}
	}
}

func TestDeregisterKeepsNewerRegistration(t *testing.T) {
	home := setTestHome(t)

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

	_, parsed := readProfilesFile(t, home)
	entry := profileEntry(t, parsed, "embedded")
	if entry["token"] != "token-newer" {
		t.Errorf("embedded token = %#v, want token-newer", entry["token"])
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

func TestConcurrentRegisterDeregisterKeepsFileWellFormed(t *testing.T) {
	home := setTestHome(t)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			token := "token-a"
			if n%2 == 1 {
				token = "token-b"
			}
			if _, err := registerEmbeddedProfile(testRegistration(token)); err != nil {
				t.Errorf("register failed: %v", err)
			}
			if _, _, err := deregisterEmbeddedProfile("token-a"); err != nil {
				t.Errorf("deregister failed: %v", err)
			}
		}(i)
	}
	wg.Wait()

	// Whatever interleaving happened, the file must parse and contain either
	// no embedded profile or a well-formed one.
	_, parsed := readProfilesFile(t, home)
	if profiles, ok := parsed["profiles"].(map[string]any); ok {
		if raw, present := profiles["embedded"]; present {
			if _, ok := raw.(map[string]any); !ok {
				t.Errorf("embedded entry is malformed: %#v", raw)
			}
		}
	}
}
