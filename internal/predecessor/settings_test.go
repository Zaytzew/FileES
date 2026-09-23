package predecessor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var carryTime = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

func carryFixture(t *testing.T) (source, target, backups string) {
	t.Helper()
	root := t.TempDir()
	source = filepath.Join(root, "msi", "config.json")
	target = filepath.Join(root, "store", "config.json")
	backups = filepath.Join(root, "migration-backups")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte(`{"from":"msi"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return source, target, backups
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestCarrySettingsCopiesAndKeepsAnExactRecoveryCopy(t *testing.T) {
	source, target, backups := carryFixture(t)
	var validated string
	result, err := CarrySettings(source, target, backups, carryTime, func(path string) error {
		validated = path
		return nil
	})
	if err != nil {
		t.Fatalf("carry: %v", err)
	}
	if !result.Carried || result.ReplacedTarget {
		t.Fatalf("result = %+v, want carried into an empty target", result)
	}
	if got := readFile(t, target); got != `{"from":"msi"}` {
		t.Fatalf("target = %q", got)
	}
	if validated != target {
		t.Fatalf("validated %q, want the written target %q", validated, target)
	}
	if got := readFile(t, filepath.Join(result.Backup, "predecessor-config.json")); got != `{"from":"msi"}` {
		t.Fatalf("recovery copy = %q", got)
	}
	// The predecessor's file stays where it was: uninstalling it is a separate
	// step, and until then it is the original.
	if got := readFile(t, source); got != `{"from":"msi"}` {
		t.Fatalf("source changed: %q", got)
	}
}

// A target that already exists is someone's configuration too. It is kept in
// the backup before being replaced, so choosing the predecessor's settings is
// never the end of the other ones.
func TestCarrySettingsBacksUpAnExistingTarget(t *testing.T) {
	source, target, backups := carryFixture(t)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(`{"from":"store"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := CarrySettings(source, target, backups, carryTime, nil)
	if err != nil {
		t.Fatalf("carry: %v", err)
	}
	if !result.ReplacedTarget {
		t.Fatal("existing target was not reported as replaced")
	}
	if got := readFile(t, filepath.Join(result.Backup, "previous-target-config.json")); got != `{"from":"store"}` {
		t.Fatalf("previous target copy = %q", got)
	}
	if got := readFile(t, target); got != `{"from":"msi"}` {
		t.Fatalf("target = %q", got)
	}
}

// The target variant's own config-check decides. If it refuses, the target is
// exactly what it was: the old file, or no file - never the refused one.
func TestCarrySettingsRestoresTheTargetWhenValidationRefuses(t *testing.T) {
	refuse := func(string) error { return errors.New("unknown field") }

	t.Run("previous file restored", func(t *testing.T) {
		source, target, backups := carryFixture(t)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(`{"from":"store"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := CarrySettings(source, target, backups, carryTime, refuse); err == nil || !strings.Contains(err.Error(), "nothing was changed") {
			t.Fatalf("err = %v, want a refusal that says nothing changed", err)
		}
		if got := readFile(t, target); got != `{"from":"store"}` {
			t.Fatalf("target after refusal = %q", got)
		}
	})

	t.Run("absent target stays absent", func(t *testing.T) {
		source, target, backups := carryFixture(t)
		if _, err := CarrySettings(source, target, backups, carryTime, refuse); err == nil {
			t.Fatal("refused configuration was accepted")
		}
		if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("refused configuration left behind: %v", err)
		}
	})
}

// A predecessor that never wrote a configuration has nothing to carry. That is
// a fresh install being replaced, not a failure, and it must not create a
// backup directory full of nothing.
func TestCarrySettingsWithoutPredecessorConfigurationIsANoOp(t *testing.T) {
	root := t.TempDir()
	result, err := CarrySettings(filepath.Join(root, "missing.json"), filepath.Join(root, "target.json"), filepath.Join(root, "backups"), carryTime, nil)
	if err != nil || result.Carried || result.Backup != "" {
		t.Fatalf("result = %+v, err = %v; want a no-op", result, err)
	}
	if _, err := os.Stat(filepath.Join(root, "backups")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup directory created for nothing: %v", err)
	}
}

func TestCarrySettingsNeverReusesABackupDirectory(t *testing.T) {
	source, target, backups := carryFixture(t)
	first, err := CarrySettings(source, target, backups, carryTime, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := CarrySettings(source, target, backups, carryTime, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Backup == second.Backup {
		t.Fatalf("two takeovers in the same second shared %s", first.Backup)
	}
}

func TestCarrySettingsRefusesRelativeAndIdenticalPaths(t *testing.T) {
	root := t.TempDir()
	same := filepath.Join(root, "config.json")
	if _, err := CarrySettings("config.json", same, root, carryTime, nil); err == nil {
		t.Fatal("relative source accepted")
	}
	if _, err := CarrySettings(same, same, root, carryTime, nil); err == nil {
		t.Fatal("identical source and target accepted")
	}
}

func TestParseKindAcceptsOnlyTheTwoVariants(t *testing.T) {
	for _, value := range []string{"msi", "store"} {
		if _, err := ParseKind(value); err != nil {
			t.Fatalf("%s rejected: %v", value, err)
		}
	}
	for _, value := range []string{"", "MSI", "appx", "winget"} {
		if _, err := ParseKind(value); err == nil {
			t.Fatalf("%q accepted", value)
		}
	}
}
