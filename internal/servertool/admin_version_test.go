package servertool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdminVersionInstallationMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, config, state, channel, release string
		warning                               bool
	}{
		{"alpha", "channel=alpha", `{"installed_release":"r1437-server"}`, "alpha", "r1437-server", false},
		{"beta same artifact", "channel=beta", `{"installed_release":"r1437-server"}`, "beta", "r1437-server", false},
		{"missing state", "channel=beta", "", "beta", "unknown", false},
		{"broken state", "channel=alpha", "{", "alpha", "unknown", true},
		{"missing config", "", "", "unknown", "unknown", true},
		{"invalid config", "secret=must-not-appear", "", "unknown", "unknown", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "install.conf")
			if tc.config != "" {
				config := "[local]\nstate_dir=" + filepath.ToSlash(dir) + "\n[repo]\nurl=https://example.test/releases\n" + tc.config + "\n"
				if err := os.WriteFile(path, []byte(config), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.state != "" {
				if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(tc.state), 0600); err != nil {
					t.Fatal(err)
				}
			}
			for _, alias := range []string{"version", "--version", "-version"} {
				var out, diag strings.Builder
				if code := RunAdmin([]string{alias, "--install-config", path}, &out, &diag); code != ExitOK {
					t.Fatalf("code=%d: %s", code, diag.String())
				}
				for _, want := range []string{"filees-admin " + adminVersion + "\n", "platform: ", "update_channel: " + tc.channel + "\n", "recorded_release: " + tc.release + "\n"} {
					if !strings.Contains(out.String(), want) {
						t.Errorf("missing %q in %s", want, out.String())
					}
				}
				if (diag.Len() > 0) != tc.warning {
					t.Errorf("warning=%q", diag.String())
				}
				if strings.Contains(out.String()+diag.String(), "must-not-appear") {
					t.Fatal("config value leaked")
				}
			}
		})
	}
}

func TestAdminVersionRejectsUnknownOptions(t *testing.T) {
	for _, args := range [][]string{{"version", "extra"}, {"--version", "--unknown"}} {
		var out, diag strings.Builder
		if got := RunAdmin(args, &out, &diag); got != ExitUsage {
			t.Fatalf("code=%d for %v", got, args)
		}
	}
}
