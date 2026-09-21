package packaging

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestServerBuildVersionStamp(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		shell = filepath.Join(os.Getenv("ProgramFiles"), "Git", "bin", "bash.exe")
		if _, err := os.Stat(shell); err != nil {
			t.Skip("POSIX shell unavailable")
		}
	}
	script, err := filepath.Abs("server-build-version.sh")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "VERSION"), []byte("0.1.17\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		revision, expected, want string
		fail                     bool
	}{
		{"1437", "", "0.1.17+r1437", false},
		{"1437M", "", "0.1.17+r1437M", false},
		{"1430:1437MSP", "", "0.1.17+r1430-1437MSP", false},
		{"exported", "", "0.1.17+unversioned", false},
		{"", "", "0.1.17+unversioned", false},
		{"1437", "1437", "0.1.17+r1437", false},
		{"1437M", "1437", "", true},
		{"1430:1437", "1437", "", true},
		{"1437S", "1437", "", true},
		{"exported", "1437", "", true},
		{"1438", "1437", "", true},
	} {
		t.Run(tc.revision+"/"+tc.expected, func(t *testing.T) {
			// Replace only SVN discovery; execute the shipped POSIX script.
			cmd := exec.Command(shell, "-c", `svnversion() { printf '%s' "$TEST_SVN_REVISION"; }; script=$1; shift; . "$script"`, "test", filepath.ToSlash(script), filepath.ToSlash(root))
			cmd.Env = append(os.Environ(), "TEST_SVN_REVISION="+tc.revision, "FILEES_SOURCE_REVISION="+tc.expected)
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.fail {
				t.Fatalf("err=%v output=%s", err, out)
			}
			if !tc.fail && strings.TrimSpace(string(out)) != tc.want {
				t.Fatalf("got %q want %q", out, tc.want)
			}
		})
	}
}

func TestServerBuildUsesRevisionStamp(t *testing.T) {
	for path, want := range map[string][]string{
		"build-server.sh":                    {`version=$(sh "$root/packaging/server-build-version.sh" "$root")`, `adminVersion=$version`, `printf '%s\n' "$version" >"$tmp/VERSION"`},
		"../tools/prepare-server-release.sh": {`FILEES_SOURCE_REVISION="$source_revision"`, `-svn-revision "$source_revision"`},
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range want {
			if !strings.Contains(string(raw), s) {
				t.Errorf("%s missing %s", path, s)
			}
		}
	}
}
