package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strconv"
)

// ArchiveFormats is a release-signer's assertion about the WHOLE payload, not
// an executable's self-report and not permission to replace a protected server.
// Producers must not emit it until the coordinated upgrade has been accepted.
// Older strict parsers reject the unknown field instead of silently ignoring it.
type ArchiveFormats struct {
	Schema        string `json:"schema"`
	Runtime       string `json:"runtime"`
	Policy        string `json:"policy"`
	Admission     string `json:"admission"`
	ReleaseAudit  string `json:"release_audit"`
	PayloadSHA256 string `json:"payload_sha256"`
}

const ArchiveFormatsSchema = "filees.archive-bundle-formats/v1"

// ArchivePayloadSHA256 binds the declaration to the canonical Go JSON encoding
// of this manifest with only archive_formats omitted. Array order is significant;
// release identity, freshness, all files, modes, ownership, configs and orphans
// are covered. BasePath is local transport context and is deliberately excluded.
// This digest does NOT authenticate a manifest: signature verification and
// freshness checks remain mandatory at the existing resolver boundary.
func (m *Manifest) ArchivePayloadSHA256() (string, error) {
	if m == nil {
		return "", errors.New("archive bundle manifest is nil")
	}
	copy := *m
	copy.ArchiveFormats = nil
	raw, err := json.Marshal(copy)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func (m *Manifest) ValidateArchiveFormats() error {
	if m == nil {
		return errors.New("archive bundle manifest is nil")
	}
	a := m.ArchiveFormats
	if a == nil {
		return nil // ordinary releases retain their existing contract
	}
	if a.Schema != ArchiveFormatsSchema || a.Runtime != "filees.archive-protection/v1" ||
		a.Policy != "filees.repository-archive-policy/v1" || a.Admission != "filees.archive-admission/v1" ||
		a.ReleaseAudit != "filees.archive-admin-release/v1" {
		return errors.New("archive bundle declares unsupported protection formats")
	}
	want, err := m.ArchivePayloadSHA256()
	if err != nil || a.PayloadSHA256 != want {
		return errors.New("archive bundle declaration does not match the complete manifest")
	}
	required := archiveRequiredBinaries()
	switch m.Platform {
	case "openbsd-amd64":
		required = append(required, "/usr/libexec/auth/login_-filees")
	case "linux-amd64":
		required = append(required, "{libexec_dir}/filees/filees-ssh-auth")
	default:
		return errors.New("archive bundle platform is not supported")
	}
	files := make(map[string]File, len(m.Files))
	for _, f := range m.Files {
		if path.Clean(f.Target) != f.Target || !validTarget(f.Target) {
			return fmt.Errorf("archive bundle has a noncanonical target %q", f.Target)
		}
		if _, duplicate := files[f.Target]; duplicate {
			return fmt.Errorf("archive bundle duplicates target %q", f.Target)
		}
		files[f.Target] = f
		for _, orphan := range m.Orphans {
			if path.Clean(orphan.Target) == f.Target {
				return fmt.Errorf("archive bundle also removes target %q", f.Target)
			}
		}
	}
	for _, target := range required {
		f, ok := files[target]
		mode, err := strconv.ParseUint(f.Mode, 8, 12)
		if !ok || f.Kind != "binary" || err != nil || mode&0500 != 0500 || mode&0222 != 0 {
			return fmt.Errorf("archive bundle lacks an immutable executable component %s", target)
		}
	}
	// Startup adapters and the Linux PAM module participate in the installation
	// too. They cannot be silently left at an unverified previous version.
	auxiliary := []string{"/etc/rc.d/filees_public_authority", "/etc/rc.d/filees_links"}
	if m.Platform == "linux-amd64" {
		auxiliary = []string{"{libexec_dir}/filees/pam_filees_tunnel.so"}
	}
	for _, target := range auxiliary {
		f, ok := files[target]
		mode, err := strconv.ParseUint(f.Mode, 8, 12)
		if !ok || f.Kind != "file" || f.Owner != "root" || err != nil || mode&0022 != 0 || mode&0400 == 0 ||
			(m.Platform == "openbsd-amd64" && (mode&0500 != 0500 || mode&0222 != 0)) {
			return fmt.Errorf("archive bundle lacks a protected startup component %s", target)
		}
	}
	return nil
}

// Keep this baseline explicit. New executables are still covered by the whole
// manifest digest; a packaging regression cannot shrink the baseline to just a
// worker with a matching --archive-guard-version response.
func archiveRequiredBinaries() []string {
	return []string{
		"{sbin_dir}/filees-admin", "{sbin_dir}/filees-operation", "{sbin_dir}/filees-install", "{sbin_dir}/filees-rotate",
		"{libexec_dir}/filees/filees-onboard", "{libexec_dir}/filees/filees-bootstrap-entry",
		"{libexec_dir}/filees/filees-mail", "{libexec_dir}/filees/filees-entry", "{libexec_dir}/filees/filees-worker",
		"{libexec_dir}/filees/filees-service-wc-corrector", "{libexec_dir}/filees/filees-client-entry",
		"{libexec_dir}/filees/filees-mobile-v1", "{libexec_dir}/filees/filees-recovery-entry",
		"{libexec_dir}/filees/filees-recovery-authorize", "{libexec_dir}/filees/filees-public-authority",
		"{libexec_dir}/filees/filees-links", "{libexec_dir}/filees/filees-serving-state",
	}
}
