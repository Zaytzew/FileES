package client

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/google/uuid"
)

// RetainedPathBinding is trusted evidence from the private release journal.
// This optional operation never discovers a backup or authorizes its retirement.
type RetainedPathBinding struct {
	SourceVolume   uint32
	SourceIndex    uint64
	RepositoryUUID string
	URL            string
	Revision       int64
}

// ExcludeRetainedPath requires the bound Windows runtime. No depth/CLI fallback,
// no cleanup/retry after uncertainty; the caller must inspect actual effects.
// It must hold the live WC guard and keep the journal/copy protected throughout.
func (c *execClient) ExcludeRetainedPath(ctx context.Context, wc, rel, retained string, b RetainedPathBinding) error {
	if runtime.GOOS != "windows" || !nativeWCOps(c) {
		return errors.ErrUnsupported
	}
	u, err := url.Parse(b.URL)
	id, idErr := uuid.Parse(b.RepositoryUUID)
	if !filepath.IsAbs(wc) || filepath.Clean(wc) != wc || !validMovePath(rel) || filepath.ToSlash(rel) != rel ||
		!filepath.IsAbs(retained) || filepath.Clean(retained) != retained || b.SourceIndex == 0 ||
		b.Revision < 1 || b.Revision > math.MaxInt32 || idErr != nil || id.String() != b.RepositoryUUID ||
		err != nil || !u.IsAbs() || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("invalid retained exclusion binding")
	}
	if err := c.RequireRetainedPathSupport(ctx); err != nil {
		return err
	}
	raw, err := c.nativeRemote(ctx, wc, "exclude-bound", "--wc", wc, "--retained", retained,
		"--source-id", fmt.Sprintf("%d:%d", b.SourceVolume, b.SourceIndex),
		"--revision", strconv.FormatInt(b.Revision, 10), "--uuid", b.RepositoryUUID, "--url", b.URL, "--", rel)
	if err != nil {
		return err
	}
	rev, err := nativeRevisionValue(raw, "revision", false)
	if err != nil || raw["state"] != "excluded" || raw["path"] != rel || rev != b.Revision {
		return errors.New("bound exclusion effect lacks an exact receipt; preserve retained copy")
	}
	return ctx.Err()
}

// RequireRetainedPathSupport allows the private coordinator to refuse an older
// runtime before converting a placeholder to an ordinary file.
func (c *execClient) RequireRetainedPathSupport(ctx context.Context) error {
	if runtime.GOOS != "windows" || !nativeWCOps(c) {
		return errors.ErrUnsupported
	}
	if err := c.nativeRequireFeature(ctx, "sparse_exclude_bound_v1"); err != nil {
		return errors.Join(errors.ErrUnsupported, err)
	}
	return ctx.Err()
}
