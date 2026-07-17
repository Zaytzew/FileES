package svnrotate

import (
	"testing"
	"time"
)

func TestParseSize(t *testing.T) {
	good := map[string]int64{
		"25GiB":  25 << 30,
		"500MiB": 500 << 20,
		"1TiB":   1 << 40,
		"64KiB":  64 << 10,
		"2g":     2 << 30,
		"10M":    10 << 20,
		"12345":  12345,
		" 1GiB ": 1 << 30,
	}
	for in, want := range good {
		got, err := ParseSize(in)
		if err != nil {
			t.Errorf("ParseSize(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseSize(%q) = %d, want %d", in, got, want)
		}
	}
	for _, in := range []string{"", "0", "-5GiB", "0MiB", "abc", "GiB", "1.5GiB"} {
		if _, err := ParseSize(in); err == nil {
			t.Errorf("ParseSize(%q): expected error", in)
		}
	}
}

func TestConfigValidateRejects(t *testing.T) {
	base := Config{
		RepoPath:      "/srv/svn/repo",
		ArchiveDir:    "/srv/svn/archive",
		SizeThreshold: 1,
		MaxAge:        time.Hour,
	}
	cases := map[string]func(*Config){
		"relative repo":     func(c *Config) { c.RepoPath = "repo" },
		"relative archive":  func(c *Config) { c.ArchiveDir = "archive" },
		"repo == archive":   func(c *Config) { c.ArchiveDir = c.RepoPath },
		"zero size":         func(c *Config) { c.SizeThreshold = 0 },
		"negative size":     func(c *Config) { c.SizeThreshold = -1 },
		"zero age":          func(c *Config) { c.MaxAge = 0 },
		"repo not a repo":   func(c *Config) {}, // /srv/svn/repo/format does not exist here
	}
	for name, mutate := range cases {
		cfg := base
		mutate(&cfg)
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestParseSvnlookDate(t *testing.T) {
	got, err := parseSvnlookDate("2026-07-17 14:22:33 +0200 (czw, 17 lip 2026)\n")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 7, 17, 12, 22, 33, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("got %s, want %s", got.UTC(), want)
	}
	if _, err := parseSvnlookDate("garbage"); err == nil {
		t.Fatal("expected error for malformed date")
	}
}
