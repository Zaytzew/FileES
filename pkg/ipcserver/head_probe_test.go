//go:build windows && native_cfapi_probe

package ipcserver

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	contract "filees/pkg/contract/v1"
)

// A disconnected real provider: listing must not need bytes or a callback.
func TestHeadListDoesNotMarkUnhydratedPlaceholderLocal(t *testing.T) {
	helper := os.Getenv("FILEES_CFAPI")
	if helper == "" {
		t.Fatal("provide an isolated native CFAPI helper")
	}
	root := t.TempDir()
	call := func(stdin string, args ...string) {
		t.Helper()
		command := exec.CommandContext(t.Context(), helper, args...)
		command.Stdin = strings.NewReader(stdin)
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v %s", args, err, out)
		}
	}
	call("", "register", "--root", root, "--identity", "head-list-probe")
	t.Cleanup(func() { _ = exec.Command(helper, "unregister", "--root", root).Run() })
	call("f\t4096\tremote.txt\tremote.txt\n", "placeholders", "--root", root)
	if err := os.WriteFile(filepath.Join(root, "local.txt"), []byte("local"), 0600); err != nil {
		t.Fatal(err)
	}
	server := New("unused")
	server.SetHeadService(&headStub{entries: []contract.RepoHeadEntry{{Name: "remote.txt", Kind: "file"}, {Name: "local.txt", Kind: "file"}}})
	server.RegisterActivation(contract.ActivationStatus{ServerID: "primary", ClientRole: contract.ClientRoleNormal})
	server.RegisterRepoAccess("repo", "svn://example/repo", root, "primary", "rw")
	result := headDecode[contract.RepoHeadListResult](t, server.dispatch(lifecycleRequest(contract.CmdRepoHeadList, contract.RepoHeadListPayload{ServerID: "primary", RepoID: "repo"})))
	if len(result.Entries) != 2 || result.Entries[0].Local || !result.Entries[1].Local {
		t.Fatalf("wrong local marks: %+v", result)
	}
}
