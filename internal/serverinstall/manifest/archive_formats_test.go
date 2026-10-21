package manifest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func archiveFormatFixture(t *testing.T, goos string) *Manifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "packaging", "server", goos+"-binary-policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m.SchemaVersion, m.ReleaseID, m.Sequence, m.SecurityEpoch = SchemaVersion, "fixture-r1", 1, 1
	for i := range m.Files {
		m.Files[i].SHA256 = strings.Repeat("ab", 32)
	}
	m.ArchiveFormats = &ArchiveFormats{Schema: ArchiveFormatsSchema, Runtime: "filees.archive-protection/v1",
		Policy: "filees.repository-archive-policy/v1", Admission: "filees.archive-admission/v1", ReleaseAudit: "filees.archive-admin-release/v1"}
	resealArchiveFixture(t, &m)
	return &m
}

func resealArchiveFixture(t *testing.T, m *Manifest) {
	t.Helper()
	digest, err := m.ArchivePayloadSHA256()
	if err != nil {
		t.Fatal(err)
	}
	m.ArchiveFormats.PayloadSHA256 = digest
}

func TestArchiveFormatsWholeReleaseBinding(t *testing.T) {
	for _, goos := range []string{"openbsd", "linux"} {
		m := archiveFormatFixture(t, goos)
		raw, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := Parse(raw)
		if err != nil {
			t.Fatal(goos, err)
		}
		parsed.BasePath = "transport/location/not/signed"
		if err := parsed.ValidateArchiveFormats(); err != nil {
			t.Fatal("local transport context changed signed binding", err)
		}
		m.ArchiveFormats = nil
		raw, _ = json.Marshal(m)
		if strings.Contains(string(raw), "archive_formats") {
			t.Fatal("ordinary releases unexpectedly advertise archive compatibility")
		}
		if _, err := Parse(raw); err != nil {
			t.Fatal("legacy release refused", err)
		}
	}
	var absent *Manifest
	if _, err := absent.ArchivePayloadSHA256(); err == nil {
		t.Fatal("nil manifest accepted")
	}
}

func TestArchiveFormatsCannotTransplantDeclaration(t *testing.T) {
	for _, change := range []string{"release", "sequence", "epoch", "platform", "source", "target", "hash", "mode", "owner", "group", "config", "orphan", "additional binary", "created_at", "order"} {
		t.Run(change, func(t *testing.T) {
			m := archiveFormatFixture(t, "openbsd")
			switch change {
			case "release":
				m.ReleaseID = "fixture-r2"
			case "sequence":
				m.Sequence++
			case "epoch":
				m.SecurityEpoch++
			case "platform":
				m.Platform = "linux-amd64"
			case "source":
				m.Files[0].Source = "bin/other"
			case "target":
				m.Files[0].Target += "-other"
			case "hash":
				m.Files[0].SHA256 = strings.Repeat("cd", 32)
			case "mode":
				m.Files[0].Mode = "0755"
			case "owner":
				m.Files[0].Owner = "another"
			case "group":
				m.Files[0].Group = "another"
			case "config":
				m.Configs[0].Name += "-changed"
			case "orphan":
				m.Orphans = append(m.Orphans, Orphan{Target: "/old/target"})
			case "additional binary":
				m.Files = append(m.Files, File{Source: "bin/new", Target: "{sbin_dir}/new", Kind: "binary"})
			case "created_at":
				m.CreatedAt = "2026-10-21T12:00:00Z"
			case "order":
				m.Files[0], m.Files[1] = m.Files[1], m.Files[0]
			}
			if err := m.ValidateArchiveFormats(); err == nil {
				t.Fatal("changed manifest accepted under an old declaration")
			}
		})
	}
}

func TestArchiveFormatsRequiresCompleteBaseline(t *testing.T) {
	for _, goos := range []string{"openbsd", "linux"} {
		base := archiveFormatFixture(t, goos)
		for _, file := range base.Files {
			if file.Kind != "binary" && file.Target != "/etc/rc.d/filees_links" && file.Target != "/etc/rc.d/filees_public_authority" && file.Target != "{libexec_dir}/filees/pam_filees_tunnel.so" {
				continue
			}
			t.Run(goos+"/"+file.Target, func(t *testing.T) {
				m := archiveFormatFixture(t, goos)
				for i, f := range m.Files {
					if f.Target == file.Target {
						m.Files = append(m.Files[:i], m.Files[i+1:]...)
						break
					}
				}
				resealArchiveFixture(t, m)
				if err := m.ValidateArchiveFormats(); err == nil {
					t.Fatal("incomplete release accepted even with a fresh digest")
				}
			})
		}
	}
}

func TestArchiveFormatsRejectsBadFormatsAndAmbiguousInventory(t *testing.T) {
	for _, change := range []string{"schema", "runtime", "policy", "admission", "release audit", "empty digest", "non-executable", "writable", "wrong kind", "duplicate", "target alias", "orphan overlap", "unknown platform", "unknown field"} {
		t.Run(change, func(t *testing.T) {
			m := archiveFormatFixture(t, "openbsd")
			switch change {
			case "schema":
				m.ArchiveFormats.Schema += "-future"
			case "runtime":
				m.ArchiveFormats.Runtime += "-future"
			case "policy":
				m.ArchiveFormats.Policy += "-future"
			case "admission":
				m.ArchiveFormats.Admission += "-future"
			case "release audit":
				m.ArchiveFormats.ReleaseAudit += "-future"
			case "non-executable":
				m.Files[0].Mode = "0444"
			case "writable":
				m.Files[0].Mode = "0755"
			case "wrong kind":
				m.Files[0].Kind = "file"
			case "duplicate":
				m.Files = append(m.Files, m.Files[0])
			case "target alias":
				m.Files[0].Target = "{sbin_dir}//filees-admin"
			case "orphan overlap":
				m.Orphans = []Orphan{{Target: m.Files[0].Target}}
			case "unknown platform":
				m.Platform = "other-amd64"
			}
			resealArchiveFixture(t, m)
			if change == "empty digest" {
				m.ArchiveFormats.PayloadSHA256 = ""
			}
			raw, _ := json.Marshal(m)
			if change == "unknown field" {
				raw = []byte(strings.Replace(string(raw), `"archive_formats":{`, `"archive_formats":{"permit_upgrade":true,`, 1))
			}
			if _, err := Parse(raw); err == nil {
				t.Fatal("unsafe archive declaration accepted")
			}
		})
	}
}

func TestArchiveFormatsStartupComponentPermissions(t *testing.T) {
	for _, goos := range []string{"linux", "openbsd"} {
		for _, change := range []string{"writable", "owner", "unreadable"} {
			t.Run(goos+"/"+change, func(t *testing.T) {
				m := archiveFormatFixture(t, goos)
				for i := range m.Files {
					if m.Files[i].Target != "/etc/rc.d/filees_links" && m.Files[i].Target != "{libexec_dir}/filees/pam_filees_tunnel.so" {
						continue
					}
					switch change {
					case "writable":
						m.Files[i].Mode = "0777"
					case "owner":
						m.Files[i].Owner = "_filees-state"
					case "unreadable":
						m.Files[i].Mode = "0000"
					}
				}
				resealArchiveFixture(t, m)
				if err := m.ValidateArchiveFormats(); err == nil {
					t.Fatal("unsafe startup component accepted")
				}
			})
		}
	}
}
