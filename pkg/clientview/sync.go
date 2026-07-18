package clientview

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Updater interface {
	Update(context.Context, string) (string, error)
}

type SyncConfig struct {
	WorkingCopy      string
	RelativeViewPath string
	CachePath        string
}

// Sync updates the read-only service working copy and publishes a validated,
// monotonic projection cache. The live cache is never changed on transport,
// parse, identity or generation failure.
func Sync(ctx context.Context, updater Updater, config SyncConfig) (View, bool, error) {
	if updater == nil {
		return View{}, false, errors.New("client view updater is required")
	}
	if !filepath.IsAbs(config.WorkingCopy) || !filepath.IsAbs(config.CachePath) {
		return View{}, false, errors.New("client view paths must be absolute")
	}
	rel := filepath.Clean(strings.TrimSpace(config.RelativeViewPath))
	if rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return View{}, false, errors.New("client view relative path is invalid")
	}
	if _, err := updater.Update(ctx, filepath.Clean(config.WorkingCopy)); err != nil {
		return View{}, false, fmt.Errorf("update service projection: %w", err)
	}
	viewPath := filepath.Join(filepath.Clean(config.WorkingCopy), rel)
	view, err := Load(viewPath)
	if err != nil {
		return View{}, false, fmt.Errorf("load service projection: %w", err)
	}
	changed, err := StoreIfNewer(filepath.Clean(config.CachePath), view)
	if err != nil {
		return View{}, false, fmt.Errorf("publish service projection: %w", err)
	}
	return view, changed, nil
}

func CachedOrNone(path string) (View, bool, error) {
	view, err := Load(path)
	if errors.Is(err, os.ErrNotExist) {
		return View{}, false, nil
	}
	return view, err == nil, err
}
