package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const DefaultConfigPath = "/etc/filees/install.conf"

type Config struct {
	ConfigPath string

	RepoURL  string
	Channel  string
	Platform string
	SVNPath  string

	StateDir  string
	StageDir  string
	BackupDir string

	SbinDir      string
	LibexecDir   string
	SysconfDir   string
	SSHDConfDir  string
	SSHKeysDir   string
	LoginConfDir string
	DataDir      string

	DefaultAction   string
	ConfigDrift     string
	OrphanFiles     string
	Interactive     bool
	RequireHash     bool
	VerifySignature bool
	SignifyProgram  string
	Talkative       bool

	// SignifyPubkey is the embedded release signing public key loaded from the
	// binary by main.go. Never read from the config file.
	SignifyPubkey []byte
}

func Find(cli string) (string, error) {
	if strings.TrimSpace(cli) != "" {
		return cli, nil
	}
	for _, p := range []string{DefaultConfigPath, "./filees-install.conf"} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("config not found (tried %s)", DefaultConfigPath)
}

func Load(path string) (*Config, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	cfg := defaults(abs)

	section := ""
	sc := bufio.NewScanner(f)
	for lineNo := 1; sc.Scan(); lineNo++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			if section == "" {
				return nil, fmt.Errorf("bad config section on line %d", lineNo)
			}
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("bad config line %d: %s", lineNo, line)
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(os.ExpandEnv(val))
		if key == "" {
			return nil, fmt.Errorf("bad config line %d: %s", lineNo, line)
		}
		if section != "" && !strings.Contains(key, ".") {
			key = section + "." + key
		}
		if err := set(cfg, key, val, lineNo); err != nil {
			return nil, err
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return cfg, cfg.finalize()
}

func defaults(abs string) *Config {
	return &Config{
		ConfigPath:      abs,
		Channel:         "stable",
		Platform:        runtime.GOOS + "-" + runtime.GOARCH,
		SVNPath:         "svn",
		StateDir:        "/var/filees/install-state",
		StageDir:        "/var/filees/install-stage",
		BackupDir:       "/var/filees/install-backup",
		SbinDir:         "/usr/local/sbin",
		LibexecDir:      "/usr/local/libexec",
		SysconfDir:      "/etc/filees",
		SSHDConfDir:     "/etc/ssh/sshd_config.d",
		SSHKeysDir:      "/etc/ssh",
		LoginConfDir:    "/etc/login.conf.d",
		DataDir:         "/var/filees",
		DefaultAction:   "check",
		ConfigDrift:     "block",
		OrphanFiles:     "keep",
		Interactive:     true,
		RequireHash:     true,
		VerifySignature: false,
		SignifyProgram:  "signify",
	}
}

func set(cfg *Config, key, val string, lineNo int) error {
	switch key {
	case "repo.url":
		cfg.RepoURL = strings.TrimRight(val, "/")
	case "repo.channel":
		cfg.Channel = val
	case "repo.platform":
		cfg.Platform = val
	case "repo.svn":
		cfg.SVNPath = val
	case "local.state_dir":
		cfg.StateDir = val
	case "local.stage_dir":
		cfg.StageDir = val
	case "local.backup_dir":
		cfg.BackupDir = val
	case "install.sbin_dir":
		cfg.SbinDir = val
	case "install.libexec_dir":
		cfg.LibexecDir = val
	case "install.sysconf_dir":
		cfg.SysconfDir = val
	case "install.sshd_conf_dir":
		cfg.SSHDConfDir = val
	case "install.ssh_keys_dir":
		cfg.SSHKeysDir = val
	case "install.login_conf_dir":
		cfg.LoginConfDir = val
	case "install.data_dir":
		cfg.DataDir = val
	case "policy.default_action":
		cfg.DefaultAction = strings.ToLower(val)
	case "policy.config_drift":
		cfg.ConfigDrift = strings.ToLower(val)
	case "policy.orphan_files":
		cfg.OrphanFiles = strings.ToLower(val)
	case "policy.interactive":
		b, err := parseBool(val)
		if err != nil {
			return fmt.Errorf("bad interactive on line %d: %w", lineNo, err)
		}
		cfg.Interactive = b
	case "policy.require_hash":
		b, err := parseBool(val)
		if err != nil {
			return fmt.Errorf("bad require_hash on line %d: %w", lineNo, err)
		}
		cfg.RequireHash = b
	case "policy.verify_signature":
		b, err := parseBool(val)
		if err != nil {
			return fmt.Errorf("bad verify_signature on line %d: %w", lineNo, err)
		}
		cfg.VerifySignature = b
	case "policy.talkative":
		b, err := parseBool(val)
		if err != nil {
			return fmt.Errorf("bad talkative on line %d: %w", lineNo, err)
		}
		cfg.Talkative = b
	case "security.signify":
		cfg.SignifyProgram = val
	default:
		return fmt.Errorf("unknown config key on line %d: %s", lineNo, key)
	}
	return nil
}

func (cfg *Config) finalize() error {
	cfg.RepoURL = strings.TrimRight(strings.TrimSpace(cfg.RepoURL), "/")
	cfg.Channel = strings.TrimSpace(cfg.Channel)
	cfg.Platform = strings.TrimSpace(cfg.Platform)
	cfg.SVNPath = strings.TrimSpace(cfg.SVNPath)
	cfg.SignifyProgram = strings.TrimSpace(cfg.SignifyProgram)
	if cfg.SignifyProgram == "" {
		cfg.SignifyProgram = "signify"
	}
	cfg.DefaultAction = strings.ToLower(strings.TrimSpace(cfg.DefaultAction))
	cfg.ConfigDrift = strings.ToLower(strings.TrimSpace(cfg.ConfigDrift))
	cfg.OrphanFiles = strings.ToLower(strings.TrimSpace(cfg.OrphanFiles))

	if cfg.RepoURL == "" {
		return fmt.Errorf("repo.url is required")
	}
	if !oneOf(cfg.DefaultAction, "check", "dry-run", "apply") {
		return fmt.Errorf("policy.default_action must be check, dry-run or apply")
	}
	if !oneOf(cfg.ConfigDrift, "block", "warn", "ignore") {
		return fmt.Errorf("policy.config_drift must be block, warn or ignore")
	}
	if !oneOf(cfg.OrphanFiles, "keep", "warn", "remove") {
		return fmt.Errorf("policy.orphan_files must be keep, warn or remove")
	}
	for _, p := range []*string{&cfg.StateDir, &cfg.StageDir, &cfg.BackupDir,
		&cfg.SbinDir, &cfg.LibexecDir, &cfg.SysconfDir,
		&cfg.SSHDConfDir, &cfg.SSHKeysDir, &cfg.DataDir} {
		*p = cleanPath(*p)
	}
	return nil
}


func cleanPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

func parseBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "yes", "true", "on":
		return true, nil
	case "0", "no", "false", "off":
		return false, nil
	default:
		return false, fmt.Errorf("expected boolean, got %q", s)
	}
}

func oneOf(v string, values ...string) bool {
	for _, x := range values {
		if v == x {
			return true
		}
	}
	return false
}
