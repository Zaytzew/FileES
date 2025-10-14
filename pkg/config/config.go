package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Repo — końcowy typ używany przez main.go i serwisy.
// Pola z czasami są już sparsowane do time.Duration.
// Nazwy odpowiadają referencjom w main.go (CommitInterval, GlobalSlots, itd.).
type Repo struct {
	ID             string        `json:"id"`
	RepoURL        string        `json:"repo_url"`
	LocalPath      string        `json:"local_path"`
	WatchInterval  time.Duration `json:"-"`                // z pola JSON "watch_interval"
	CommitInterval time.Duration `json:"-"`                // z pola JSON "commit_interval"
	Username       string        `json:"username,omitempty"`
	Password       string        `json:"password,omitempty"`

	// Opcjonalne rozszerzenia (mogą nie wystąpić w JSON; wtedy wartości domyślne/zero)
	GlobalSlots    int           `json:"global_slots,omitempty"`
	MaxBatchFiles  int           `json:"max_batch_files,omitempty"`
	LockFirst      bool          `json:"lock_first,omitempty"`
	ShoutPatterns  []string      `json:"shout_patterns,omitempty"`
	RateLimitShout time.Duration `json:"-"` // z pola JSON "rate_limit_shout"
}

// jsonRepo — struktura pomocnicza do dekodowania JSON (czasy jako stringi).
type jsonRepo struct {
	ID             string `json:"id"`
	RepoURL        string `json:"repo_url"`
	LocalPath      string `json:"local_path"`
	WatchInterval  string `json:"watch_interval"`
	CommitInterval string `json:"commit_interval"`
	Username       string `json:"username,omitempty"`
	Password       string `json:"password,omitempty"`

	GlobalSlots    int      `json:"global_slots,omitempty"`
	MaxBatchFiles  int      `json:"max_batch_files,omitempty"`
	LockFirst      bool     `json:"lock_first,omitempty"`
	ShoutPatterns  []string `json:"shout_patterns,omitempty"`
	RateLimitShout string   `json:"rate_limit_shout,omitempty"`
}

// Load — wczytuje listę repozytoriów z JSON i dokonuje walidacji + konwersji pól.
func Load(path string) ([]Repo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("plik konfiguracyjny %q nie istnieje", path)
		}
		return nil, fmt.Errorf("nie udało się odczytać %q: %w", path, err)
	}

	var raw []jsonRepo
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("błąd parsowania JSON %q: %w", path, err)
	}

	out := make([]Repo, 0, len(raw))
	for i, r := range raw {
		if strings.TrimSpace(r.ID) == "" {
			return nil, fmt.Errorf("config[%d]: brak pola 'id'", i)
		}
		if strings.TrimSpace(r.RepoURL) == "" {
			return nil, fmt.Errorf("config[%d]: brak pola 'repo_url'", i)
		}
		if strings.TrimSpace(r.LocalPath) == "" {
			return nil, fmt.Errorf("config[%d]: brak pola 'local_path'", i)
		}

		watch, err := parseDurationNonEmpty(r.WatchInterval)
		if err != nil { return nil, fmt.Errorf("config[%d].watch_interval: %w", i, err) }
		commit, err := parseDurationNonEmpty(r.CommitInterval)
		if err != nil { return nil, fmt.Errorf("config[%d].commit_interval: %w", i, err) }

		var shoutRate time.Duration
		if strings.TrimSpace(r.RateLimitShout) != "" {
			shoutRate, err = time.ParseDuration(strings.TrimSpace(r.RateLimitShout))
			if err != nil {
				return nil, fmt.Errorf("config[%d].rate_limit_shout: %w", i, err)
			}
		}

		out = append(out, Repo{
			ID:             r.ID,
			RepoURL:        r.RepoURL,
			LocalPath:      filepath.Clean(r.LocalPath),
			WatchInterval:  watch,
			CommitInterval: commit,
			Username:       r.Username,
			Password:       r.Password,
			GlobalSlots:    r.GlobalSlots,
			MaxBatchFiles:  r.MaxBatchFiles,
			LockFirst:      r.LockFirst,
			ShoutPatterns:  dedupTrim(r.ShoutPatterns),
			RateLimitShout: shoutRate,
		})
	}
	return out, nil
}

func parseDurationNonEmpty(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" { return 0, errors.New("puste") }
	return time.ParseDuration(s)
}

func dedupTrim(in []string) []string {
	m := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, p := range in {
		q := strings.TrimSpace(p)
		if q == "" { continue }
		if _, ok := m[q]; ok { continue }
		m[q] = struct{}{}
		out = append(out, q)
	}
	return out
}

// MustCompileRegex łączy listę wzorców w jeden wyrażeniowy OR i kompiluje do *regexp.Regexp.
// Zwraca zawsze ważny *regexp.Regexp (w najgorszym wypadku dopasowujący do niczego),
// aby uprościć wywołania w main.go.
func MustCompileRegex(patterns []string) *regexp.Regexp {
	if len(patterns) == 0 {
		return regexp.MustCompile(`$a`) // nigdy nie pasuje
	}
	parts := make([]string, 0, len(patterns))
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" { continue }
		// Każdy wzorzec zostaje wzięty w (?: ... )
		parts = append(parts, "(?:"+p+")")
	}
	if len(parts) == 0 {
		return regexp.MustCompile(`$a`)
	}
	re, err := regexp.Compile(strings.Join(parts, "|"))
	if err != nil {
		// W razie błędu składni — fallback do wzorca niepasującego
		return regexp.MustCompile(`$a`)
	}
	return re
}
