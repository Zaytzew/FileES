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

// Cleaner releases a working copy left locked by an interrupted operation. It
// is optional in the way TreeSource is optional beside Source: an updater that
// cannot clean keeps working and simply reports the lock.
type Cleaner interface {
	Cleanup(context.Context, string) (string, error)
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
	workingCopy := filepath.Clean(config.WorkingCopy)
	if _, err := updater.Update(ctx, workingCopy); err != nil {
		// A lock left by an interrupted operation is ours to clear. This
		// directory belongs to FileES, not to the person using it, and
		// Subversion states the remedy in the error itself - "Run 'svn
		// cleanup'" - which nobody was doing and nobody was being told.
		//
		// Measured 2026-09-03: the service working copy for one server stayed
		// locked, every projection sync failed on it, and the interface
		// reported the server as not responding. The server was never
		// contacted; the fault was entirely local, and the same cleanup-then-
		// update that Checkout has always performed would have cleared it.
		cleaner, canClean := updater.(Cleaner)
		if !canClean || !isWorkingCopyLocked(err) {
			return View{}, false, fmt.Errorf("update service projection: %w", err)
		}
		if _, cleanErr := cleaner.Cleanup(ctx, workingCopy); cleanErr != nil {
			return View{}, false, fmt.Errorf("release locked service projection: %w", cleanErr)
		}
		// Once. A lock that survives its own cleanup is a different fault and
		// must be reported rather than retried around.
		if _, err := updater.Update(ctx, workingCopy); err != nil {
			return View{}, false, fmt.Errorf("update service projection after cleanup: %w", err)
		}
	}
	viewPath := filepath.Join(workingCopy, rel)
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

// isWorkingCopyLocked recognises Subversion's own report that a working copy
// is held by an interrupted operation. Matching the code rather than the
// sentence keeps this working under a translated svn, which is what the daemon
// actually runs on a Polish desktop.
func isWorkingCopyLocked(err error) bool {
	if err == nil {
		return false
	}
	low := strings.ToLower(err.Error())
	return strings.Contains(low, "e155004") || strings.Contains(low, "svn cleanup")
}
