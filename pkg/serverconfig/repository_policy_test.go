package serverconfig

import (
	"path/filepath"
	"testing"
)

func TestRepositoryPolicyDefaultsAndExplicitDataErasureWindow(t *testing.T) {
	var repository RepositoryFile
	if got := repository.EffectiveDataErasureMaxDays(); got != 90 {
		t.Fatalf("default data-erasure window = %d, want 90", got)
	}
	days := 180
	repository.DataErasureMaxDays = &days
	if got := repository.EffectiveDataErasureMaxDays(); got != days {
		t.Fatalf("explicit data-erasure window = %d, want %d", got, days)
	}
}

func TestRepositorySVNMuccIsSiblingOfConfiguredSVNAdmin(t *testing.T) {
	repository := RepositoryFile{SVNAdminBinary: filepath.Join(string(filepath.Separator), "usr", "local", "bin", "svnadmin")}
	want := filepath.Join(filepath.Dir(repository.SVNAdminBinary), "svnmucc")
	if got := repository.EffectiveSVNMuccBinary(); got != want {
		t.Fatalf("svnmucc=%q want=%q", got, want)
	}
	if got := (RepositoryFile{}).EffectiveSVNMuccBinary(); got != "" {
		t.Fatalf("empty svnadmin derived svnmucc=%q", got)
	}
}

func TestRepositoryWhaleRootDefaultsAndCanUseCapacityFilesystem(t *testing.T) {
	results := filepath.Join(string(filepath.Separator), "var", "filees", "results")
	repository := RepositoryFile{ResultsRoot: results}
	if got, want := repository.EffectiveWhaleRoot(), filepath.Join(results, "whale"); got != want {
		t.Fatalf("default whale root=%q want=%q", got, want)
	}
	capacity := filepath.Join(string(filepath.Separator), "storage", "filees-whale")
	repository.WhaleRoot = capacity
	if got := repository.EffectiveWhaleRoot(); got != capacity {
		t.Fatalf("explicit whale root=%q want=%q", got, capacity)
	}
}

func TestPublicShareServerBoundaryRequiresHTTPSAndLoopbackOrUnix(t *testing.T) {
	root := t.TempDir()
	valid := PublicSharesFile{Enabled: true, BaseURL: "https://get.example.test", StateRoot: filepath.Join(root, "state"), FrostKeyFile: filepath.Join(root, "frost.key"), AuthorityStagingRoot: filepath.Join(root, "staging"), BackchannelNetwork: "tcp", BackchannelAddress: "127.0.0.1:9010"}
	if err := validatePublicShares(valid, root); err != nil {
		t.Fatal(err)
	}
	public := valid
	public.BackchannelAddress = "0.0.0.0:9010"
	if err := validatePublicShares(public, root); err == nil {
		t.Fatal("public authority backchannel accepted")
	}
	badPort := valid
	badPort.BackchannelAddress = "127.0.0.1:99999"
	if err := validatePublicShares(badPort, root); err == nil {
		t.Fatal("out-of-range authority port accepted")
	}
	badSize := valid
	badSize.MaxLeafSize = -1
	if err := validatePublicShares(badSize, root); err == nil {
		t.Fatal("negative public leaf size accepted")
	}
	badChannels := valid
	badChannels.MaxChannelsPerRealm = -1
	if err := validatePublicShares(badChannels, root); err == nil {
		t.Fatal("negative public channel limit accepted")
	}
	insecure := valid
	insecure.BaseURL = "http://get.example.test"
	if err := validatePublicShares(insecure, root); err == nil {
		t.Fatal("non-TLS public origin accepted")
	}
	unix := valid
	unix.BackchannelNetwork, unix.BackchannelAddress, unix.BackchannelSocketGroup = "unix", filepath.Join(root, "authority.sock"), "filees-public"
	if err := validatePublicShares(unix, root); err != nil {
		t.Fatal(err)
	}
}
