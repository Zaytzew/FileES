package svnrotate

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultSizeThreshold = 25 << 30 // 25 GiB
	DefaultMaxAge        = 365 * 24 * time.Hour
)

// Config controls rotation triggers and paths. The working directory is
// deliberately NOT configurable: it is always a fresh MkdirTemp inside
// ArchiveDir, so cleanup can never RemoveAll a caller-supplied path.
type Config struct {
	RepoPath   string
	ArchiveDir string

	SizeThreshold int64         // bytes after pack; must be > 0
	MaxAge        time.Duration // age of hot-repo r1 svn:date; must be > 0

	BreakLocks bool // proceed despite active locks (edit passports die)

	// DumpDepth > 0 additionally archives a dump.gz of the last DumpDepth
	// revisions before HEAD (clamped to r1). The frozen FSFS archive is the
	// canonical full-history artifact; the dump is a bounded recovery
	// window per backup policy, never a second full copy. 0 disables.
	DumpDepth int
}

func (c *Config) Validate() error {
	if !filepath.IsAbs(c.RepoPath) {
		return fmt.Errorf("repo path must be absolute, got %q", c.RepoPath)
	}
	if !filepath.IsAbs(c.ArchiveDir) {
		return fmt.Errorf("archive dir must be absolute, got %q", c.ArchiveDir)
	}
	c.RepoPath = filepath.Clean(c.RepoPath)
	c.ArchiveDir = filepath.Clean(c.ArchiveDir)
	if c.RepoPath == c.ArchiveDir {
		return fmt.Errorf("repo and archive must be different directories")
	}
	if c.SizeThreshold <= 0 {
		return fmt.Errorf("size threshold must be > 0, got %d", c.SizeThreshold)
	}
	if c.MaxAge <= 0 {
		return fmt.Errorf("max age must be > 0, got %s", c.MaxAge)
	}
	if c.DumpDepth < 0 {
		return fmt.Errorf("dump depth must be >= 0, got %d", c.DumpDepth)
	}
	if _, err := os.Stat(filepath.Join(c.RepoPath, "format")); err != nil {
		return fmt.Errorf("%s does not look like an SVN repository: %w", c.RepoPath, err)
	}
	return nil
}

// ParseSize parses a human-readable size: plain bytes, or a KiB/MiB/GiB/TiB
// (also single-letter K/M/G/T) suffix. The result must be positive.
func ParseSize(s string) (int64, error) {
	raw := strings.TrimSpace(s)
	upper := strings.ToUpper(raw)
	mult := int64(1)
	for _, suf := range []struct {
		name string
		mult int64
	}{
		{"KIB", 1 << 10}, {"MIB", 1 << 20}, {"GIB", 1 << 30}, {"TIB", 1 << 40},
		{"K", 1 << 10}, {"M", 1 << 20}, {"G", 1 << 30}, {"T", 1 << 40},
	} {
		if strings.HasSuffix(upper, suf.name) {
			mult = suf.mult
			upper = strings.TrimSpace(strings.TrimSuffix(upper, suf.name))
			break
		}
	}
	n, err := strconv.ParseInt(upper, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("bad size %q", raw)
	}
	if n <= 0 {
		return 0, fmt.Errorf("size must be positive, got %q", raw)
	}
	if n > (1<<63-1)/mult {
		return 0, fmt.Errorf("size %q overflows", raw)
	}
	return n * mult, nil
}

// ShouldRotate packs the repository, measures its on-disk size, and checks
// the age of the oldest revision's svn:date (per the concept — not a marker
// file's mtime). Returns (rotate, reason, error).
func ShouldRotate(cfg Config, logw io.Writer) (bool, string, error) {
	if err := cfg.Validate(); err != nil {
		return false, "", err
	}
	if err := pack(cfg.RepoPath); err != nil {
		return false, "", fmt.Errorf("pack: %w", err)
	}
	size, err := diskSize(cfg.RepoPath)
	if err != nil {
		return false, "", fmt.Errorf("disk size: %w", err)
	}
	if size >= cfg.SizeThreshold {
		return true, fmt.Sprintf("size %.1f GiB ≥ threshold %.1f GiB",
			float64(size)/(1<<30), float64(cfg.SizeThreshold)/(1<<30)), nil
	}

	head, err := headRev(cfg.RepoPath)
	if err != nil {
		return false, "", err
	}
	if head == 0 {
		return false, "", nil // empty repo: nothing to age out
	}
	oldest, err := revDate(cfg.RepoPath, 1)
	if err != nil {
		return false, "", fmt.Errorf("oldest revision date: %w", err)
	}
	if age := time.Since(oldest); age >= cfg.MaxAge {
		return true, fmt.Sprintf("oldest revision age %.0f days ≥ max-age %.0f days",
			age.Hours()/24, cfg.MaxAge.Hours()/24), nil
	}
	return false, "", nil
}

func diskSize(path string) (int64, error) {
	var total int64
	err := filepath.WalkDir(path, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}
