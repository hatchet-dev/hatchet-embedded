package embed

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// The embedded engine registers itself as a Hatchet CLI profile named
// "embedded" in ~/.hatchet/profiles.yaml when it becomes ready, and removes
// the registration again on graceful shutdown. This lets the hatchet CLI and
// its MCP server discover a running embedded engine through the ordinary
// profile store instead of environment handshakes or port probes.
//
// The CLI cannot share its profile-store code with this module (it lives
// under an internal/ path), so the file format is written directly here:
//
//   - The CLI loads the file leniently via viper (no strict decoding), so the
//     extra metadata fields written below (embedded, pid, cwd, startedat) are
//     ignored by released CLI versions; the registration itself shows up in
//     `hatchet profile list` and is directly usable.
//   - Keys are written lowercased to match what viper's own writer produces;
//     viper reads keys case-insensitively either way.
//   - expiresat and startedat must serialize as unquoted YAML timestamps: the
//     CLI decodes profiles into a struct with time.Time fields, and a quoted
//     string there would fail its (otherwise lenient) unmarshalling.
//
// Registration is best effort: any error is logged as a warning by the caller
// and never fails engine startup. Removal is token matched, so an instance
// that shut down late never deletes a newer instance's registration.
// Last-writer-wins for the single "embedded" name is intended.

// embeddedProfileName is the reserved CLI profile name for the embedded engine.
const embeddedProfileName = "embedded"

const (
	profilesLockTimeout     = 5 * time.Second
	profilesLockRetryDelay  = 50 * time.Millisecond
	profilesLockMaxAttempts = 100
)

// embeddedProfileEntry is the profile written to the CLI profile store. The
// first block mirrors the CLI's released profile schema; the second block is
// metadata the CLI ignores.
type embeddedProfileEntry struct {
	TenantID     string    `yaml:"tenantid"`
	Name         string    `yaml:"name"`
	Token        string    `yaml:"token"`
	ExpiresAt    time.Time `yaml:"expiresat"`
	APIServerURL string    `yaml:"apiserverurl"`
	GrpcHostPort string    `yaml:"grpchostport"`
	TLSStrategy  string    `yaml:"tlsstrategy"`

	Embedded  bool      `yaml:"embedded"`
	PID       int       `yaml:"pid"`
	Cwd       string    `yaml:"cwd,omitempty"`
	StartedAt time.Time `yaml:"startedat"`
}

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
	dir, path, err := profilesFilePath()
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("could not create %s: %w", dir, err)
	}

	unlock, err := acquireProfilesLock(dir)
	if err != nil {
		return "", err
	}
	defer unlock()

	doc, root, err := loadProfilesDocument(path)
	if err != nil {
		return "", err
	}

	profiles, err := mappingValueOrCreate(root, "profiles")
	if err != nil {
		return "", err
	}

	cwd, _ := os.Getwd()
	entryNode, err := yamlNodeFor(embeddedProfileEntry{
		TenantID:     reg.tenantID,
		Name:         embeddedProfileName,
		Token:        reg.token,
		ExpiresAt:    reg.expiresAt.UTC(),
		APIServerURL: reg.apiURL,
		GrpcHostPort: reg.grpcAddress,
		TLSStrategy:  "none",
		Embedded:     true,
		PID:          os.Getpid(),
		Cwd:          cwd,
		StartedAt:    time.Now().UTC(),
	})
	if err != nil {
		return "", err
	}

	setMappingValue(profiles, embeddedProfileName, entryNode)

	// Force block style on the mappings we touch: an empty "profiles: {}"
	// left behind by a previous deregistration parses as flow style, and flow
	// style would make the encoder quote the timestamp values, which the CLI
	// cannot decode into its time.Time fields.
	root.Style = 0
	profiles.Style = 0

	// The user's defaultprofile setting is intentionally never touched.
	if err := writeYAMLAtomic(path, doc); err != nil {
		return "", err
	}

	return path, nil
}

// deregisterEmbeddedProfile removes the "embedded" profile from the CLI
// profile store, but only when its token still matches this instance's token:
// a newer instance's registration (last writer wins) is left alone. Reports
// whether an entry was removed and the path acted on.
func deregisterEmbeddedProfile(token string) (bool, string, error) {
	dir, path, err := profilesFilePath()
	if err != nil {
		return false, "", err
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return false, path, nil
	}

	unlock, err := acquireProfilesLock(dir)
	if err != nil {
		return false, path, err
	}
	defer unlock()

	doc, root, err := loadProfilesDocument(path)
	if err != nil {
		return false, path, err
	}

	profiles := mappingValue(root, "profiles")
	if profiles == nil || profiles.Kind != yaml.MappingNode {
		return false, path, nil
	}

	entry := mappingValue(profiles, embeddedProfileName)
	if entry == nil || entry.Kind != yaml.MappingNode {
		return false, path, nil
	}

	entryToken := mappingValue(entry, "token")
	if entryToken == nil || entryToken.Value != token {
		return false, path, nil
	}

	deleteMappingKey(profiles, embeddedProfileName)

	// A defaultprofile setting pointing at "embedded" is left as is: changing
	// the user's default is out of scope here, and both the CLI and the MCP
	// server tolerate a default that names a missing profile.
	if err := writeYAMLAtomic(path, doc); err != nil {
		return false, path, err
	}

	return true, path, nil
}

// profilesFilePath resolves the CLI profile store location the same way the
// CLI does: ~/.hatchet plus the profile file name from the
// HATCHET_CLI_PROFILE_FILE_NAME env var or the profileFileName key in
// ~/.hatchet/config.yaml, defaulting to profiles.yaml.
func profilesFilePath() (dir string, path string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("could not determine the home directory: %w", err)
	}

	dir = filepath.Join(home, ".hatchet")

	name := os.Getenv("HATCHET_CLI_PROFILE_FILE_NAME")
	if name == "" {
		name = profileFileNameFromConfig(filepath.Join(dir, "config.yaml"))
	}
	if name == "" {
		name = "profiles.yaml"
	}

	return dir, filepath.Join(dir, name), nil
}

// profileFileNameFromConfig reads the profileFileName key (any casing) from
// the CLI config file, returning "" when the file or key is absent or
// unreadable.
func profileFileNameFromConfig(configPath string) string {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return ""
	}

	var conf map[string]any
	if err := yaml.Unmarshal(data, &conf); err != nil {
		return ""
	}

	for key, value := range conf {
		if strings.EqualFold(key, "profilefilename") {
			if name, ok := value.(string); ok {
				return name
			}
		}
	}

	return ""
}

// acquireProfilesLock takes the CLI's config lock (~/.hatchet/config.lock)
// with the same protocol the CLI uses: exclusive create, retry every 50ms up
// to 100 attempts, and locks older than 5s are considered stale.
func acquireProfilesLock(dir string) (func(), error) {
	lockFile := filepath.Join(dir, "config.lock")

	for attempts := 0; attempts < profilesLockMaxAttempts; attempts++ {
		f, err := os.OpenFile(lockFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_, writeErr := f.WriteString(time.Now().Format(time.RFC3339))
			_ = f.Close()
			if writeErr != nil {
				_ = os.Remove(lockFile)
				return nil, fmt.Errorf("could not write lock file: %w", writeErr)
			}
			return func() { _ = os.Remove(lockFile) }, nil
		}

		if os.IsExist(err) {
			if stat, statErr := os.Stat(lockFile); statErr == nil && time.Since(stat.ModTime()) > profilesLockTimeout {
				_ = os.Remove(lockFile)
				continue
			}
		} else {
			return nil, fmt.Errorf("could not create lock file: %w", err)
		}

		time.Sleep(profilesLockRetryDelay)
	}

	return nil, fmt.Errorf("could not acquire the profile store lock at %s", lockFile)
}

// loadProfilesDocument parses the profiles file into a yaml document whose
// root is a mapping, creating an empty document when the file is absent or
// empty. Parse failures are returned rather than overwriting the file.
func loadProfilesDocument(path string) (doc *yaml.Node, root *yaml.Node, err error) {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, fmt.Errorf("could not read %s: %w", path, err)
	}

	if len(bytes.TrimSpace(data)) == 0 {
		root = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		doc = &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}
		return doc, root, nil
	}

	doc = &yaml.Node{}
	if err := yaml.Unmarshal(data, doc); err != nil {
		return nil, nil, fmt.Errorf("could not parse %s (leaving it untouched): %w", path, err)
	}

	if len(doc.Content) == 0 {
		root = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		doc = &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}
		return doc, root, nil
	}

	root = doc.Content[0]
	if root.Kind == yaml.ScalarNode && root.Tag == "!!null" {
		*root = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		return doc, root, nil
	}
	if root.Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("%s has an unexpected structure (top level is not a mapping); leaving it untouched", path)
	}

	return doc, root, nil
}

// yamlNodeFor round-trips v through the yaml encoder to obtain its node
// representation, so values like time.Time serialize exactly as the encoder
// would write them (unquoted timestamps).
func yamlNodeFor(v any) (*yaml.Node, error) {
	data, err := yaml.Marshal(v)
	if err != nil {
		return nil, err
	}

	doc := &yaml.Node{}
	if err := yaml.Unmarshal(data, doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, fmt.Errorf("could not build a yaml node")
	}

	return doc.Content[0], nil
}

// mappingValue returns the value node for key in mapping m, matching keys
// case-insensitively (viper treats profile keys case-insensitively).
func mappingValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}

	for i := 0; i+1 < len(m.Content); i += 2 {
		if strings.EqualFold(m.Content[i].Value, key) {
			return m.Content[i+1]
		}
	}

	return nil
}

// mappingValueOrCreate returns the mapping value for key, creating an empty
// mapping entry when the key is absent or null.
func mappingValueOrCreate(m *yaml.Node, key string) (*yaml.Node, error) {
	value := mappingValue(m, key)
	if value == nil {
		value = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		m.Content = append(m.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
			value,
		)
		return value, nil
	}

	if value.Kind == yaml.ScalarNode && value.Tag == "!!null" {
		*value = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		return value, nil
	}
	if value.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("the %q key in the profiles file is not a mapping; leaving the file untouched", key)
	}

	return value, nil
}

// setMappingValue replaces the value for key in mapping m (case-insensitive),
// appending the pair when the key is absent.
func setMappingValue(m *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if strings.EqualFold(m.Content[i].Value, key) {
			m.Content[i+1] = value
			return
		}
	}

	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		value,
	)
}

// deleteMappingKey removes key (case-insensitive) and its value from mapping m.
func deleteMappingKey(m *yaml.Node, key string) bool {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if strings.EqualFold(m.Content[i].Value, key) {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return true
		}
	}

	return false
}

// writeYAMLAtomic writes doc to path via a same-directory temp file and
// rename, so concurrent readers never see a partial file. Two concurrent
// writers still race whole-file (last rename wins); with the config lock held
// this only matters against writers that ignore the lock, and the loser's
// write is a well-formed file.
func writeYAMLAtomic(path string, doc *yaml.Node) error {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(4)
	if err := enc.Encode(doc); err != nil {
		_ = enc.Close()
		return fmt.Errorf("could not encode the profiles file: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("could not encode the profiles file: %w", err)
	}

	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf("could not write %s: %w", tmp, err)
	}

	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("could not replace %s: %w", path, err)
	}

	return nil
}
