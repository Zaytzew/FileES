package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"

	"github.com/google/uuid"
)

// ConversionBoundReader is metadata-only; local and remote identities must be
// compared before the source is retained. No inferred UUID or HEAD fallback.
type ConversionBoundReader interface {
	InspectConversionRoot(context.Context, string) (ConversionRootInfo, error)
	CheckoutDepthEmptyAt(context.Context, string, string, int64) (string, error)
}
type ConversionRootInfo struct {
	URL, UUID string
	Revision  int64
}

func (c *execClient) InspectConversionRoot(ctx context.Context, target string) (ConversionRootInfo, error) {
	if c.nativeSVNPath == "" {
		return ConversionRootInfo{}, errors.New("conversion root inspection requires native SVN")
	}
	if err := c.nativeRequireFeature(ctx, "conversion_inventory_v1"); err != nil {
		return ConversionRootInfo{}, err
	}
	info, err := c.nativeTargetInfo(ctx, target)
	if err != nil {
		return ConversionRootInfo{}, err
	}
	if info.Kind != "dir" || uuid.Validate(info.ReposUUID) != nil || info.Revision < 0 || info.URL == "" {
		return ConversionRootInfo{}, errors.New("conversion root identity is incomplete")
	}
	return ConversionRootInfo{info.URL, info.ReposUUID, info.Revision}, nil
}

// CheckoutDepthEmptyAt never checks out HEAD or invokes a full update when
// retrying. The caller owns an existing, guarded destination without a WC.
func (c *execClient) CheckoutDepthEmptyAt(ctx context.Context, url, wc string, revision int64) (string, error) {
	if c.nativeSVNPath == "" || !filepath.IsAbs(wc) || revision < 0 {
		return "", errors.New("bound sparse checkout requires native SVN and an explicit revision")
	}
	if err := c.nativeRequireFeature(ctx, "conversion_inventory_v1"); err != nil {
		return "", err
	}
	if err := c.nativeRequireFeature(ctx, "sparse_checkout_v1"); err != nil {
		return "", err
	}
	if info, err := os.Lstat(wc); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("bound checkout requires the caller's existing ordinary directory")
	}
	if _, err := os.Lstat(filepath.Join(wc, ".svn")); !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("bound checkout refuses an existing or unreadable working copy")
	}
	raw, err := c.nativeRemote(ctx, "", "checkout", "--url", url, "--wc", wc, "--force", "--depth", "empty", "--revision", strconv.FormatInt(revision, 10))
	if err != nil {
		return "", err
	}
	return nativeUpdateReceipt(raw)
}
