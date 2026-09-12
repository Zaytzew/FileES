package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/internal/svnurl"
	"filees/pkg/client"
	"filees/pkg/clientprofile"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
	"filees/pkg/provisioning"
)

// Opt-in desktop acceptance: real IPC, lifecycle, provisioner and C transport;
// only remote channel authority and its URL route are synthetic. No user server.
func TestShelfDesktopAcceptance(t *testing.T) {
	root := os.Getenv("FILEES_SHELF_DESKTOP_LAB")
	if root == "" {
		t.Skip("interactive isolated desktop acceptance")
	}
	if !filepath.IsAbs(root) {
		t.Fatal("absolute lab root required")
	}
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(root, 0700))
	url := svnurl.File(filepath.Join(root, "repository"))
	run := func(args ...string) {
		out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v %s", args[0], err, out)
		}
	}
	_, existingRepo := os.Stat(filepath.Join(root, "repository"))
	if os.IsNotExist(existingRepo) {
		run("svnadmin", "create", filepath.Join(root, "repository"))
	}
	seed := filepath.Join(root, "seed")
	must(os.MkdirAll(filepath.Join(seed, "incoming", "nested"), 0700))
	data := []byte("FileES isolated shelf acceptance\n")
	must(os.WriteFile(filepath.Join(seed, "incoming", "nested", "selected.txt"), data, 0600))
	must(os.WriteFile(filepath.Join(seed, "incoming", "nested", "not-selected.txt"), []byte("must remain remote"), 0600))
	if os.IsNotExist(existingRepo) {
		run("svn", "import", "-q", "-m", "synthetic shelf", seed, url)
	}
	store, err := localrepo.Open(filepath.Join(root, "lifecycle.json"))
	must(err)
	provisioningStore, err := provisioning.NewStore(filepath.Join(root, "provisioning"))
	must(err)
	p := newDaemonProvisioner(store, provisioningStore, nil)
	p.AddProfile(clientprofile.Profile{ServerID: "shelf-lab"})
	p.newAttachmentSVN = func(clientprofile.Profile, string) attachmentSVN {
		return shelfDesktopSVN{client.New(client.Options{SvnPath: "svn", NativeSVNPath: os.Getenv("FILEES_TEST_NATIVE_SPARSE")}), url}
	}
	s := ipcserver.New(filepath.Join(root, "ipc.sock"))
	s.RegisterActivation(contract.ActivationStatus{ServerID: "shelf-lab", DisplayName: "TEST PÓŁKI — bez Twoich repozytoriów", RealmID: "owner", RealmAlias: "test", ClientRole: contract.ClientRoleNormal, CanCreateRepositories: true, ViewSyncedAt: time.Now().UTC().Format(time.RFC3339), ViewGeneration: 1})
	s.RegisterProjectedRepoPolicy("parent", "Test odbioru półki", "svn+ssh://synthetic/parent", "shelf-lab", "rw", "active", "owner", "optional", true)
	r := s.RegisterProjectedRepoPolicy("shelf", "Półka testowa", "svn+ssh://synthetic/shelf", "shelf-lab", "r", "active", "owner", "optional", false)
	r.SetPurpose(contract.RepoPurposeUploadShelf)
	s.SetUploadChannelService(shelfDesktopAuthority{item: contract.ShelfItem{UploadID: "synthetic-upload", RepoPath: "incoming/nested/selected.txt", OriginalName: "selected.txt", Size: int64(len(data)), SHA256: fmt.Sprintf("%x", sha256.Sum256(data)), Revision: 1, AcceptedAt: time.Now().UTC().Format(time.RFC3339)}})
	s.SetRepositoryLifecycleService(repositoryLifecycleService{store: store, onCreate: p.Enqueue})
	must(s.Start(t.Context()))
	go p.Run(t.Context())
	t.Log("desktop lab ready", root)
	<-t.Context().Done()
}

type shelfDesktopAuthority struct {
	ipcserver.UploadChannelService
	item contract.ShelfItem
}

func (s shelfDesktopAuthority) ListUploadChannels(context.Context, string, string) (contract.UploadChannelListResult, error) {
	return contract.UploadChannelListResult{Channels: []contract.UploadChannelSummary{{ChannelID: "channel", AuthorityRepoID: "parent", UploadRepoID: "shelf", Slug: "test-odbioru", State: "active"}}}, nil
}
func (s shelfDesktopAuthority) ListShelf(context.Context, string, string) (contract.ShelfListResult, error) {
	return contract.ShelfListResult{ChannelID: "channel", Items: []contract.ShelfItem{s.item}}, nil
}

type shelfDesktopSVN struct {
	client.Client
	url string
}

func (s shelfDesktopSVN) CheckoutDepthEmpty(ctx context.Context, _ string, path string) (string, error) {
	return s.Client.(interface {
		CheckoutDepthEmpty(context.Context, string, string) (string, error)
	}).CheckoutDepthEmpty(ctx, s.url, path)
}
func (s shelfDesktopSVN) FetchSparsePath(ctx context.Context, root, path string) (string, error) {
	return s.Client.(interface {
		FetchSparsePath(context.Context, string, string) (string, error)
	}).FetchSparsePath(ctx, root, path)
}
func (s shelfDesktopSVN) GetInfo(ctx context.Context, path string) (string, error) {
	out, err := s.Client.GetInfo(ctx, path)
	return strings.ReplaceAll(out, s.url, "svn+ssh://synthetic/shelf"), err
}
