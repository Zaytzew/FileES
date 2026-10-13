package servertool

import (
	"path/filepath"
	"time"

	"filees/internal/storagewatch"
	v1 "filees/pkg/reservation/v1"
	"filees/pkg/serverconfig"
)

// Invoked only after broker authorization of the server selector. One statfs
// on the configured repository root, no traversal, labels or global disks.
func attachStorageCapacity(c serverconfig.Config, req v1.Request, result *v1.Result, now time.Time, sample func(string) (storagewatch.Sample, error)) {
	if !req.IncludeStorageCapacity || req.Schema != v1.StateSchema || req.RepoID != "" || result.ServerID == "" {
		return
	}
	result.StorageCapacity = &v1.StorageCapacity{State: "unknown"}
	if !filepath.IsAbs(c.Repositories.Root) {
		return
	}
	measured, err := sample(c.Repositories.Root)
	if err != nil {
		return
	}
	stamp := now.UTC()
	value := v1.StorageCapacity{State: "observed", TotalBytes: measured.Total, AvailableBytes: measured.Available, MeasuredAt: &stamp, ValidForSeconds: 180}
	if value.Validate() == nil {
		result.StorageCapacity = &value
	}
}
