package repoworker

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

// A demo realm may hold at most its quota across all of its repositories
// (implementation notes (not distributed), portion 4). Commits reach a
// repository through svnserve, svnmucc and the worker alike, so the only
// place that sees every one of them is the repository's pre-commit hook. The
// hook is the worker itself, linked under that name like the lock guards.
//
// The hook runs as whatever user svnserve runs as and reads nothing but the
// repositories root: each repository of a quota realm carries a small marker
// naming its realm, and the realm's usage is the on-disk size of every
// repository with the same marker. A transaction being committed is already
// on disk when pre-commit runs, so it counts without being measured apart.

const (
	RealmQuotaSchema     = "filees.realm-quota/v1"
	realmQuotaMarkerName = "filees-realm-quota.json"
	realmQuotaHook       = "pre-commit"
)

type RealmQuotaMarker struct {
	Schema     string `json:"schema"`
	RealmID    string `json:"realm_id"`
	QuotaBytes int64  `json:"quota_bytes"`
}

// InstallRealmQuota writes the realm marker and links pre-commit to the
// worker. It never replaces a foreign pre-commit hook, and repeating it is a
// no-op.
func InstallRealmQuota(repository, executable, realmID string, quotaBytes int64) error {
	if parsed, err := uuid.Parse(realmID); err != nil || parsed.String() != realmID {
		return errors.New("realm quota realm_id must be a canonical UUID")
	}
	if quotaBytes <= 0 {
		return errors.New("realm quota must be positive")
	}
	if err := CheckLockGuardExecutable(executable); err != nil {
		return err
	}
	hooks, err := lockGuardDirectory(repository)
	if err != nil {
		return err
	}
	return WithFileLock(filepath.Join(hooks, ".filees-lock-guards.lock"), func() error {
		marker := RealmQuotaMarker{Schema: RealmQuotaSchema, RealmID: realmID, QuotaBytes: quotaBytes}
		path := filepath.Join(hooks, realmQuotaMarkerName)
		if existing, err := readRealmQuotaMarker(path); err == nil {
			if existing != marker {
				return errors.New("repository already belongs to another realm quota")
			}
		} else if errors.Is(err, os.ErrNotExist) {
			raw, _ := json.Marshal(marker)
			if err := atomicBytes(path, append(raw, '\n')); err != nil {
				return err
			}
			// The hook runs as the SVN server user, not as the worker.
			if err := os.Chmod(path, 0o644); err != nil {
				return err
			}
		} else {
			return err
		}
		hook := filepath.Join(hooks, realmQuotaHook)
		info, err := os.Lstat(hook)
		switch {
		case errors.Is(err, os.ErrNotExist):
			if err := os.Symlink(executable, hook); err != nil {
				return err
			}
		case err != nil:
			return err
		case info.Mode()&os.ModeSymlink == 0:
			return fmt.Errorf("preserving existing %s: explicit hook integration required", realmQuotaHook)
		default:
			target, err := os.Readlink(hook)
			if err != nil {
				return err
			}
			if target != executable {
				return fmt.Errorf("preserving existing %s: explicit hook integration required", realmQuotaHook)
			}
		}
		return syncDirectory(hooks)
	})
}

func readRealmQuotaMarker(path string) (RealmQuotaMarker, error) {
	file, err := os.Open(path)
	if err != nil {
		return RealmQuotaMarker{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 4096))
	decoder.DisallowUnknownFields()
	var marker RealmQuotaMarker
	if err := decoder.Decode(&marker); err != nil || marker.Schema != RealmQuotaSchema || marker.QuotaBytes <= 0 {
		return RealmQuotaMarker{}, errors.New("realm quota marker is invalid")
	}
	if parsed, err := uuid.Parse(marker.RealmID); err != nil || parsed.String() != marker.RealmID {
		return RealmQuotaMarker{}, errors.New("realm quota marker is invalid")
	}
	return marker, nil
}

// RealmQuotaUsage is the on-disk size of every repository under root that
// carries realmID's marker.
func RealmQuotaUsage(repositoriesRoot, realmID string) (int64, error) {
	entries, err := os.ReadDir(repositoriesRoot)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		repository := filepath.Join(repositoriesRoot, entry.Name())
		marker, err := readRealmQuotaMarker(filepath.Join(repository, "hooks", realmQuotaMarkerName))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("%s: %w", entry.Name(), err)
		}
		if marker.RealmID != realmID {
			continue
		}
		size, err := treeSize(repository)
		if err != nil {
			return 0, err
		}
		total += size
	}
	return total, nil
}

func treeSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			// A transaction directory can vanish while another commit
			// finishes; what is gone no longer occupies space.
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}

// RunRealmQuotaGuard is the pre-commit hook body: REPOS TXN. A repository
// without a marker belongs to no quota realm and commits freely.
func RunRealmQuotaGuard(args []string, stderr io.Writer) int {
	if len(args) != 2 || !filepath.IsAbs(args[0]) {
		fmt.Fprintln(stderr, "FileES: pre-commit expects REPOS TXN")
		return 1
	}
	repository := filepath.Clean(args[0])
	marker, err := readRealmQuotaMarker(filepath.Join(repository, "hooks", realmQuotaMarkerName))
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, "FileES: realm storage limit cannot be checked:", err)
		return 1
	}
	used, err := RealmQuotaUsage(filepath.Dir(repository), marker.RealmID)
	if err != nil {
		fmt.Fprintln(stderr, "FileES: realm storage limit cannot be checked:", err)
		return 1
	}
	if used > marker.QuotaBytes {
		fmt.Fprintf(stderr, "FileES: this demo account is limited to %d MB of storage; the commit would use %d MB.\n", marker.QuotaBytes>>20, (used+(1<<20)-1)>>20)
		return 1
	}
	return 0
}
