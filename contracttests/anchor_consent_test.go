package contracttests

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
	"filees/pkg/ipcserver"
)

func TestAnchorConsentContractIsScopedAndOptional(t *testing.T) {
	socket := testSocketPath(t)
	server := ipcserver.New(socket)
	server.RegisterActivation(contract.ActivationStatus{ServerID: "office"})
	repo := server.RegisterRepoAccess("repo", "svn://example/repo", t.TempDir(), "office", "r")
	id := strings.Repeat("ab", 16)
	repo.SetAnchorBlocksSource(func() []contract.AnchorDownloadBlock {
		return []contract.AnchorDownloadBlock{{ID: id, Program: "reader.exe", Files: 21}}
	})
	calls := 0
	server.SetAnchorConsent(func(_ context.Context, p contract.RepoAnchorConsentPayload) error {
		calls++
		if p.BlockID != id {
			return errors.New("expired")
		}
		return nil
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cli := ipcclient.New(socket, "consent-test")
	hello, err := cli.Hello(ctx)
	if err != nil || !slices.Contains(hello.Capabilities, contract.CapRepoAnchorConsent) {
		t.Fatal(hello, err)
	}
	status, err := cli.RepoStatus(ctx, "repo")
	if err != nil || len(status.AnchorDownloadBlocks) != 1 || status.AnchorDownloadBlocks[0].ID != id {
		t.Fatal(status, err)
	}
	for _, allow := range []bool{false, true} {
		result, err := cli.AnchorConsent(ctx, contract.RepoAnchorConsentPayload{ServerID: "office", RepoID: "repo", BlockID: id, Allow: allow})
		if err != nil || result.Allowed != allow {
			t.Fatal(result, err)
		}
	}
	for _, variant := range []string{"server", "repo", "access", "detached", "bad-id", "expired", "service"} {
		p := contract.RepoAnchorConsentPayload{ServerID: "office", RepoID: "repo", BlockID: id, Allow: true}
		before := calls
		switch variant {
		case "server":
			p.ServerID = "elsewhere"
		case "repo":
			p.RepoID = "elsewhere"
		case "access":
			repo.SetProjection("svn://example/repo", "")
		case "detached":
			repo.SetProjectedMetadata("repo", "svn://example/repo", "r", "unattached", "", "optional", false)
		case "bad-id":
			p.BlockID = "any-program"
		case "expired":
			p.BlockID = strings.Repeat("cc", 16)
		case "service":
			server.SetAnchorConsent(nil)
		}
		if _, err := cli.AnchorConsent(ctx, p); err == nil {
			t.Fatal("invalid consent admitted", variant)
		}
		if variant != "expired" && calls != before {
			t.Fatal("refusal reached callback", variant)
		}
		repo.SetProjectedMetadata("repo", "svn://example/repo", "r", "active", "", "optional", true)
	}
	hello, err = cli.Hello(ctx)
	if err != nil || slices.Contains(hello.Capabilities, contract.CapRepoAnchorConsent) {
		t.Fatal("absent consent service advertised")
	}
}
