package storagewatch

import (
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func policyFixture(t *testing.T, raw string) (RepositoryPolicy, string) {
	t.Helper()
	root := t.TempDir()
	p := RepositoryPolicy{Root: filepath.Join(root, "repos"), JournalRoot: filepath.Join(root, "journal")}
	path := filepath.Join(p.Root, uuid.NewString(), "conf", "svnserve.conf")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(raw), 0640); err != nil {
		t.Fatal(err)
	}
	return p, path
}

func TestCapacityRepositoryPolicyRoundTripAndOperatorRO(t *testing.T) {
	original := "# keep comment\r\n[general]\r\nanon-access = none\r\nauth-access = write\r\nauthz-db = /trusted/authz\r\n"
	p, path := policyFixture(t, original)
	manual := filepath.Join(p.Root, uuid.NewString(), "conf", "svnserve.conf")
	if err := os.MkdirAll(filepath.Dir(manual), 0700); err != nil {
		t.Fatal(err)
	}
	const manualConfig = "[general]\nauth-access = read\n"
	if err := os.WriteFile(manual, []byte(manualConfig), 0640); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := p.Hold(); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != strings.Replace(original, "auth-access = write", "auth-access = read", 1) {
		t.Fatal(string(raw))
	}
	if err := p.Restore(); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	if string(raw) != original {
		t.Fatal(string(raw))
	}
	raw, _ = os.ReadFile(manual)
	if string(raw) != manualConfig {
		t.Fatal("manual RO overwritten")
	}
	if err := p.Restore(); err != nil {
		t.Fatal("recovery not idempotent", err)
	}
}

func TestCapacityRepositoryPolicyDoesNotOverwriteOperatorEdit(t *testing.T) {
	p, path := policyFixture(t, "[general]\nauth-access = write\n")
	if err := p.Hold(); err != nil {
		t.Fatal(err)
	}
	const edit = "[general]\nauth-access = none\n"
	if err := os.WriteFile(path, []byte(edit), 0640); err != nil {
		t.Fatal(err)
	}
	if err := p.Restore(); err == nil {
		t.Fatal("operator edit overwritten")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != edit {
		t.Fatal(string(raw))
	}
	entries, _ := os.ReadDir(p.JournalRoot)
	if len(entries) != 1 {
		t.Fatal("backup lost")
	}
}

func TestCapacityRepositoryPolicyRejectsAmbiguousConfig(t *testing.T) {
	for _, raw := range []string{"[general]\n", "[general]\nauth-access = read\nauth-access = write\n", "[general]\nauth-access = write\n[general]\nauth-access = write\n", "[general]\nauth-access = other\n"} {
		p, _ := policyFixture(t, raw)
		if err := p.Hold(); err == nil {
			t.Fatal("accepted", raw)
		}
	}
}
