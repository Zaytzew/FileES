package servertool

import (
	"encoding/json"
	"errors"
	"filees/internal/storagewatch"
	v1 "filees/pkg/reservation/v1"
	"filees/pkg/serverconfig"
	"strings"
	"testing"
	"time"
)

func TestStorageCapacitySamplesOnlyOptedInAuthorizedServerRoot(t *testing.T) {
	c := serverconfig.Config{}
	c.Repositories.Root = t.TempDir()
	now := time.Now().UTC()
	calls := 0
	sample := func(path string) (storagewatch.Sample, error) {
		calls++
		if path != c.Repositories.Root {
			t.Fatal(path)
		}
		return storagewatch.Sample{Total: 1000, Available: 250}, nil
	}
	request := v1.Request{Schema: v1.StateSchema, IncludeStorageCapacity: true}
	for _, req := range []v1.Request{{Schema: v1.StateSchema}, {Schema: v1.StateSchema, RepoID: "repo", IncludeStorageCapacity: true}} {
		result := v1.Result{ServerID: "spot"}
		attachStorageCapacity(c, req, &result, now, sample)
		if result.StorageCapacity != nil {
			t.Fatal("unexpected emission")
		}
	}
	denied := v1.Result{}
	attachStorageCapacity(c, request, &denied, now, sample)
	if calls != 0 {
		t.Fatal("unauthorized/legacy request sampled storage")
	}
	result := v1.Result{ServerID: "spot"}
	attachStorageCapacity(c, request, &result, now, sample)
	if calls != 1 || result.StorageCapacity.AvailableBytes != 250 {
		t.Fatal(calls, result)
	}
	raw, _ := json.Marshal(result.StorageCapacity)
	for _, secret := range []string{c.Repositories.Root, "device", "path", "email"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("private fields on wire")
		}
	}
	attachStorageCapacity(c, request, &result, now, func(string) (storagewatch.Sample, error) { return storagewatch.Sample{}, errors.New("denied") })
	if result.StorageCapacity.State != "unknown" {
		t.Fatal(result)
	}
}
