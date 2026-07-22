package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
)

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}
var sha256Pattern = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

func stripBOM(data []byte) []byte { return bytes.TrimPrefix(data, utf8BOM) }

type Channel struct {
	SchemaVersion int    `json:"schema_version"`
	ReleaseID     string `json:"release_id"`
	Manifest      string `json:"manifest"`
	SVNRevision   string `json:"svn_revision,omitempty"`
}

type Manifest struct {
	SchemaVersion int              `json:"schema_version"`
	ReleaseID     string           `json:"release_id"`
	Platform      string           `json:"platform"`
	SVNRevision   string           `json:"svn_revision,omitempty"`
	CreatedAt     string           `json:"created_at,omitempty"`
	Files         []File           `json:"files"`
	Configs       []ConfigContract `json:"configs,omitempty"`
	Orphans       []Orphan         `json:"orphans,omitempty"`

	BasePath string `json:"-"`
}

type File struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Kind   string `json:"kind,omitempty"`
	Mode   string `json:"mode,omitempty"`
	SHA256 string `json:"sha256"`
}

type ConfigContract struct {
	Name            string          `json:"name"`
	Path            string          `json:"path"`
	Example         string          `json:"example,omitempty"`
	RequiredKeys    []string        `json:"required_keys,omitempty"`
	RecommendedKeys []string        `json:"recommended_keys,omitempty"`
	DeprecatedKeys  []string        `json:"deprecated_keys,omitempty"`
	RemovedKeys     []string        `json:"removed_keys,omitempty"`
	DefaultChanged  []DefaultChange `json:"default_changed,omitempty"`
}

type DefaultChange struct {
	Key string `json:"key"`
	Old string `json:"old,omitempty"`
	New string `json:"new,omitempty"`
}

type Orphan struct {
	Target string `json:"target"`
	Reason string `json:"reason,omitempty"`
}

func ParseChannel(data []byte) (*Channel, error) {
	var ch Channel
	if err := decodeStrict(stripBOM(data), &ch); err != nil {
		return nil, err
	}
	if ch.SchemaVersion != 1 {
		return nil, fmt.Errorf("unsupported channel schema_version %d", ch.SchemaVersion)
	}
	if strings.TrimSpace(ch.ReleaseID) == "" {
		return nil, fmt.Errorf("channel release_id is required")
	}
	if strings.TrimSpace(ch.Manifest) == "" {
		ch.Manifest = fmt.Sprintf("releases/%s/{platform}/manifest.json", ch.ReleaseID)
	}
	return &ch, nil
}

func Parse(data []byte) (*Manifest, error) {
	var m Manifest
	if err := decodeStrict(stripBOM(data), &m); err != nil {
		return nil, err
	}
	if m.SchemaVersion != 1 {
		return nil, fmt.Errorf("unsupported manifest schema_version %d", m.SchemaVersion)
	}
	if strings.TrimSpace(m.ReleaseID) == "" {
		return nil, fmt.Errorf("manifest release_id is required")
	}
	if strings.TrimSpace(m.Platform) == "" {
		return nil, fmt.Errorf("manifest platform is required")
	}
	if len(m.Files) == 0 {
		return nil, fmt.Errorf("manifest files are required")
	}
	for i, f := range m.Files {
		if strings.TrimSpace(f.Source) == "" {
			return nil, fmt.Errorf("manifest file %d source is required", i)
		}
		if strings.TrimSpace(f.Target) == "" {
			return nil, fmt.Errorf("manifest file %d target is required", i)
		}
		if !sha256Pattern.MatchString(strings.TrimSpace(f.SHA256)) {
			return nil, fmt.Errorf("manifest file %d sha256 must be 64 hexadecimal characters", i)
		}
	}
	return &m, nil
}

func decodeStrict(data []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("document contains trailing JSON value")
		}
		return fmt.Errorf("document contains trailing data: %w", err)
	}
	return nil
}

func ReleaseManifestPath(releaseID, platform string) string {
	return fmt.Sprintf("releases/%s/%s/manifest.json", releaseID, platform)
}

func ChannelPath(channel string) string {
	return fmt.Sprintf("channels/%s.json", channel)
}

func SigPath(path string) string { return path + ".sig" }

func ExpandPlatform(path, platform string) string {
	return strings.ReplaceAll(path, "{platform}", platform)
}

// Dirs maps manifest target variables to config-supplied paths.
type Dirs struct {
	SbinDir      string
	LibexecDir   string
	SysconfDir   string
	SSHDConfDir  string
	SSHKeysDir   string
	LoginConfDir string
}

func ResolveTarget(dirs Dirs, target string) string {
	out := strings.TrimSpace(target)
	repl := map[string]string{
		"{sbin_dir}":       dirs.SbinDir,
		"{libexec_dir}":    dirs.LibexecDir,
		"{sysconf_dir}":    dirs.SysconfDir,
		"{sshd_conf_dir}":  dirs.SSHDConfDir,
		"{ssh_keys_dir}":   dirs.SSHKeysDir,
		"{login_conf_dir}": dirs.LoginConfDir,
	}
	for k, v := range repl {
		out = strings.ReplaceAll(out, k, v)
	}
	return filepath.Clean(out)
}

func ResolveOrphanTarget(dirs Dirs, o Orphan) string { return ResolveTarget(dirs, o.Target) }

func (m *Manifest) SourcePath(source string) string {
	source = strings.TrimLeft(strings.TrimSpace(source), "/")
	if source == "" || strings.TrimSpace(m.BasePath) == "" {
		return source
	}
	return filepath.ToSlash(filepath.Join(m.BasePath, source))
}
