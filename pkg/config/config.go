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

// TierSpec opisuje jeden przedział rozmiaru w size-adaptive commit interval.
type TierSpec struct {
	MaxMB    float64       // górna granica w MiB (0 = catch-all, pasuje do wszystkiego)
	Interval time.Duration // minimalny odstęp między commitami dla batchy w tym przedziale
}

// Repo — końcowy typ używany przez main.go i serwisy.
// Pola z czasami są już sparsowane do time.Duration.
// Nazwy odpowiadają referencjom w main.go (CommitInterval, GlobalSlots, itd.).
type Repo struct {
	ID             string        `json:"id"`
	RepoURL        string        `json:"repo_url"`
	LocalPath      string        `json:"local_path"`
	WatchInterval  time.Duration `json:"-"` // z pola JSON "watch_interval"
	CommitInterval time.Duration `json:"-"` // z pola JSON "commit_interval"
	Username       string        `json:"username,omitempty"`
	Password       string        `json:"password,omitempty"`

	// Opcjonalne rozszerzenia (mogą nie wystąpić w JSON; wtedy wartości domyślne/zero)
	GlobalSlots            int           `json:"global_slots,omitempty"`
	MaxBatchFiles          int           `json:"max_batch_files,omitempty"`
	MaxBatchMiB            float64       `json:"max_batch_mib,omitempty"`
	BacklogFlushMiB        float64       `json:"backlog_flush_mib,omitempty"`
	ShutdownCommitTimeout  time.Duration `json:"-"`
	LockFirst              bool          `json:"lock_first,omitempty"`
	EditPassports          bool          `json:"edit_passports,omitempty"`
	EditPassportTTL        time.Duration `json:"-"`
	EditPassportHeartbeat  time.Duration `json:"-"`
	EditPassportMaxSession time.Duration `json:"-"`
	EditPassportCloseGrace time.Duration `json:"-"`
	ShoutPatterns          []string      `json:"shout_patterns,omitempty"`
	RateLimitShout         time.Duration `json:"-"` // z pola JSON "rate_limit_shout"
	CommitTiers            []TierSpec    `json:"-"` // z pola JSON "commit_tiers"
	PollInterval           time.Duration `json:"-"` // z pola JSON "poll_interval"; 0 = użyj domyślnego (30s)
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

	GlobalSlots            int      `json:"global_slots,omitempty"`
	MaxBatchFiles          int      `json:"max_batch_files,omitempty"`
	MaxBatchMiB            float64  `json:"max_batch_mib,omitempty"`
	BacklogFlushMiB        float64  `json:"backlog_flush_mib,omitempty"`
	ShutdownCommitTimeout  string   `json:"shutdown_commit_timeout,omitempty"`
	LockFirst              bool     `json:"lock_first,omitempty"`
	EditPassports          bool     `json:"edit_passports,omitempty"`
	EditPassportTTL        string   `json:"edit_passport_ttl,omitempty"`
	EditPassportHeartbeat  string   `json:"edit_passport_heartbeat,omitempty"`
	EditPassportMaxSession string   `json:"edit_passport_max_session,omitempty"`
	EditPassportCloseGrace string   `json:"edit_passport_close_grace,omitempty"`
	ShoutPatterns          []string `json:"shout_patterns,omitempty"`
	RateLimitShout         string   `json:"rate_limit_shout,omitempty"`
	PollInterval           string   `json:"poll_interval,omitempty"`
	CommitTiers            []struct {
		MaxMB    float64 `json:"max_mb"`
		Interval string  `json:"interval"`
	} `json:"commit_tiers,omitempty"`
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
	ids := make(map[string]int, len(raw))
	for i, r := range raw {
		id := strings.TrimSpace(r.ID)
		if id == "" {
			return nil, fmt.Errorf("config[%d]: brak pola 'id'", i)
		}
		if prev, ok := ids[id]; ok {
			return nil, fmt.Errorf("config[%d].id: duplikat %q (pierwszy wpis: config[%d])", i, id, prev)
		}
		ids[id] = i
		repoURL := strings.TrimSpace(r.RepoURL)
		if repoURL == "" {
			return nil, fmt.Errorf("config[%d]: brak pola 'repo_url'", i)
		}
		localPath := strings.TrimSpace(r.LocalPath)
		if localPath == "" {
			return nil, fmt.Errorf("config[%d]: brak pola 'local_path'", i)
		}
		if !filepath.IsAbs(localPath) {
			return nil, fmt.Errorf("config[%d].local_path: wymagana ścieżka bezwzględna: %q", i, localPath)
		}
		localPath, err = canonicalPath(localPath)
		if err != nil {
			return nil, fmt.Errorf("config[%d].local_path: %w", i, err)
		}

		var watch time.Duration
		if s := strings.TrimSpace(r.WatchInterval); s != "" {
			watch, err = time.ParseDuration(s)
			if err != nil || watch <= 0 {
				return nil, fmt.Errorf("config[%d].watch_interval: wymagana dodatnia wartość duration", i)
			}
		}
		commit, err := parseDurationNonEmpty(r.CommitInterval)
		if err != nil {
			return nil, fmt.Errorf("config[%d].commit_interval: %w", i, err)
		}

		var shoutRate time.Duration
		if strings.TrimSpace(r.RateLimitShout) != "" {
			shoutRate, err = time.ParseDuration(strings.TrimSpace(r.RateLimitShout))
			if err != nil || shoutRate < 0 {
				return nil, fmt.Errorf("config[%d].rate_limit_shout: wymagana nieujemna wartość duration", i)
			}
		}

		var pollInterval time.Duration
		if s := strings.TrimSpace(r.PollInterval); s != "" {
			pollInterval, err = time.ParseDuration(s)
			if err != nil || pollInterval <= 0 {
				return nil, fmt.Errorf("config[%d].poll_interval: wymagana dodatnia wartość duration", i)
			}
		}
		var shutdownTimeout time.Duration
		if s := strings.TrimSpace(r.ShutdownCommitTimeout); s != "" {
			shutdownTimeout, err = time.ParseDuration(s)
			if err != nil || shutdownTimeout <= 0 {
				return nil, fmt.Errorf("config[%d].shutdown_commit_timeout: wymagana dodatnia wartość duration", i)
			}
		}
		passportTTL, err := parseOptionalPositiveDuration(r.EditPassportTTL)
		if err != nil {
			return nil, fmt.Errorf("config[%d].edit_passport_ttl: %w", i, err)
		}
		passportHeartbeat, err := parseOptionalPositiveDuration(r.EditPassportHeartbeat)
		if err != nil {
			return nil, fmt.Errorf("config[%d].edit_passport_heartbeat: %w", i, err)
		}
		passportMax, err := parseOptionalPositiveDuration(r.EditPassportMaxSession)
		if err != nil {
			return nil, fmt.Errorf("config[%d].edit_passport_max_session: %w", i, err)
		}
		passportGrace, err := parseOptionalPositiveDuration(r.EditPassportCloseGrace)
		if err != nil {
			return nil, fmt.Errorf("config[%d].edit_passport_close_grace: %w", i, err)
		}
		effectiveTTL := durationDefault(passportTTL, 15*time.Minute)
		effectiveHeartbeat := durationDefault(passportHeartbeat, 5*time.Minute)
		effectiveMax := durationDefault(passportMax, 24*time.Hour)
		if effectiveHeartbeat >= effectiveTTL {
			return nil, fmt.Errorf("config[%d]: edit_passport_heartbeat musi być krótszy niż edit_passport_ttl", i)
		}
		if effectiveMax < effectiveTTL {
			return nil, fmt.Errorf("config[%d]: edit_passport_max_session musi być >= edit_passport_ttl", i)
		}
		if r.MaxBatchFiles < 0 || r.MaxBatchMiB < 0 || r.BacklogFlushMiB < 0 {
			return nil, fmt.Errorf("config[%d]: limity batcha nie mogą być ujemne", i)
		}
		if r.MaxBatchMiB > 0 && r.BacklogFlushMiB > 0 && r.BacklogFlushMiB < r.MaxBatchMiB {
			return nil, fmt.Errorf("config[%d]: backlog_flush_mib musi być >= max_batch_mib", i)
		}

		for j, pattern := range r.ShoutPatterns {
			if _, rerr := regexp.Compile(strings.TrimSpace(pattern)); rerr != nil {
				return nil, fmt.Errorf("config[%d].shout_patterns[%d]: %w", i, j, rerr)
			}
		}

		var tiers []TierSpec
		var previousMax float64
		seenCatchAll := false
		for j, t := range r.CommitTiers {
			d, terr := time.ParseDuration(strings.TrimSpace(t.Interval))
			if terr != nil || d <= 0 {
				return nil, fmt.Errorf("config[%d].commit_tiers[%d]: wymagana dodatnia wartość interval", i, j)
			}
			if t.MaxMB < 0 {
				return nil, fmt.Errorf("config[%d].commit_tiers[%d].max_mb: wartość nie może być ujemna", i, j)
			}
			if seenCatchAll {
				return nil, fmt.Errorf("config[%d].commit_tiers[%d]: tier po max_mb=0 jest nieosiągalny", i, j)
			}
			if t.MaxMB == 0 {
				seenCatchAll = true
			} else if j > 0 && t.MaxMB <= previousMax {
				return nil, fmt.Errorf("config[%d].commit_tiers[%d].max_mb: wartości muszą rosnąć", i, j)
			}
			tiers = append(tiers, TierSpec{MaxMB: t.MaxMB, Interval: d})
			previousMax = t.MaxMB
		}

		out = append(out, Repo{
			ID:                     id,
			RepoURL:                repoURL,
			LocalPath:              localPath,
			WatchInterval:          watch,
			CommitInterval:         commit,
			Username:               r.Username,
			Password:               r.Password,
			GlobalSlots:            r.GlobalSlots,
			MaxBatchFiles:          r.MaxBatchFiles,
			MaxBatchMiB:            r.MaxBatchMiB,
			BacklogFlushMiB:        r.BacklogFlushMiB,
			ShutdownCommitTimeout:  shutdownTimeout,
			LockFirst:              r.LockFirst,
			EditPassports:          r.EditPassports,
			EditPassportTTL:        passportTTL,
			EditPassportHeartbeat:  passportHeartbeat,
			EditPassportMaxSession: passportMax,
			EditPassportCloseGrace: passportGrace,
			ShoutPatterns:          dedupTrim(r.ShoutPatterns),
			RateLimitShout:         shoutRate,
			PollInterval:           pollInterval,
			CommitTiers:            tiers,
		})
	}
	if err := validateDisjointRoots(out); err != nil {
		return nil, err
	}
	return out, nil
}

func parseOptionalPositiveDuration(raw string) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || d <= 0 {
		return 0, errors.New("wymagana dodatnia wartość duration")
	}
	return d, nil
}

func durationDefault(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}

func canonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return filepath.Clean(resolved), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("nie można rozwiązać symlinków dla %q: %w", path, err)
	}
	return abs, nil
}

func validateDisjointRoots(repos []Repo) error {
	for i := 0; i < len(repos); i++ {
		for j := i + 1; j < len(repos); j++ {
			if pathsOverlap(repos[i].LocalPath, repos[j].LocalPath) {
				return fmt.Errorf(
					"config: repozytoria %q i %q mają nakładające się korzenie: %q i %q; zagnieżdżanie monitorowanych katalogów jest zabronione",
					repos[i].ID, repos[j].ID, repos[i].LocalPath, repos[j].LocalPath,
				)
			}
		}
	}
	return nil
}

func pathsOverlap(a, b string) bool {
	rel, err := filepath.Rel(a, b)
	if err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))) {
		return true
	}
	rel, err = filepath.Rel(b, a)
	return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))))
}

func parseDurationNonEmpty(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("puste")
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	if d <= 0 {
		return 0, errors.New("wymagana dodatnia wartość duration")
	}
	return d, nil
}

func dedupTrim(in []string) []string {
	m := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, p := range in {
		q := strings.TrimSpace(p)
		if q == "" {
			continue
		}
		if _, ok := m[q]; ok {
			continue
		}
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
		if p == "" {
			continue
		}
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
