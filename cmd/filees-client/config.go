package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	defaultSVN = "/usr/local/bin/svn"
	defaultSSH = "/usr/bin/ssh"
)

// Config is the closed invocation profile. Tool paths default to the
// OpenBSD locations and are never taken from PATH. Working copies listed
// here are the realm set. only=!wc-01 !wc-02 narrows that set; without
// only, every listed copy is reachable and no other path is.
type Config struct {
	SVN, SSH             string
	Identity, KnownHosts string
	Host                 string
	Port                 int
	Copies               map[string]workingCopySpec
	Only                 []string
}

type workingCopySpec struct {
	ID, Path, URL string
}

func loadConfig(path string) (Config, error) {
	if !filepath.IsAbs(path) {
		return Config{}, errors.New("config path must be absolute")
	}
	file, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer file.Close()
	cfg := Config{SVN: defaultSVN, SSH: defaultSSH, Port: 22, Copies: map[string]workingCopySpec{}}
	section := ""
	wcID := ""
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section, wcID, err = parseSection(line)
			if err != nil {
				return Config{}, err
			}
			if section == "wc" {
				cfg.Copies[wcID] = workingCopySpec{ID: wcID}
			}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return Config{}, fmt.Errorf("bad config line %q", line)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch section {
		case "tools":
			switch key {
			case "svn":
				cfg.SVN = value
			case "ssh":
				cfg.SSH = value
			default:
				return Config{}, fmt.Errorf("unknown tools key %q", key)
			}
		case "realm":
			switch key {
			case "identity":
				cfg.Identity = value
			case "known_hosts":
				cfg.KnownHosts = value
			case "host":
				cfg.Host = value
			case "port":
				cfg.Port, err = strconv.Atoi(value)
				if err != nil {
					return Config{}, fmt.Errorf("realm.port: %w", err)
				}
			case "only":
				cfg.Only, err = parseOnly(value)
				if err != nil {
					return Config{}, err
				}
			default:
				return Config{}, fmt.Errorf("unknown realm key %q", key)
			}
		case "wc":
			spec := cfg.Copies[wcID]
			switch key {
			case "path":
				spec.Path = value
			case "url":
				spec.URL = value
			default:
				return Config{}, fmt.Errorf("unknown wc key %q", key)
			}
			cfg.Copies[wcID] = spec
		case "":
			if key != "only" {
				return Config{}, fmt.Errorf("unknown key %q", key)
			}
			cfg.Only, err = parseOnly(value)
			if err != nil {
				return Config{}, err
			}
		default:
			return Config{}, fmt.Errorf("unknown section %q", section)
		}
	}
	if err := scanner.Err(); err != nil {
		return Config{}, err
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func parseSection(line string) (section, wcID string, err error) {
	body := strings.TrimSpace(line[1 : len(line)-1])
	if body == "tools" || body == "realm" {
		return body, "", nil
	}
	name, id, ok := strings.Cut(body, " ")
	if !ok || name != "wc" {
		return "", "", fmt.Errorf("unknown section %q", body)
	}
	id = strings.Trim(strings.TrimSpace(id), `"`)
	if id == "" || strings.ContainsAny(id, " \t!") {
		return "", "", fmt.Errorf("bad working-copy id %q", id)
	}
	return "wc", id, nil
}

// parseOnly accepts "!wc-01 !wc-02". The bang marks an explicit allow-list.
func parseOnly(value string) ([]string, error) {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return nil, errors.New("only is empty")
	}
	out := make([]string, 0, len(fields))
	seen := map[string]bool{}
	for _, field := range fields {
		if !strings.HasPrefix(field, "!") || len(field) == 1 {
			return nil, fmt.Errorf("only entry %q must look like !wc-01", field)
		}
		id := strings.TrimPrefix(field, "!")
		if seen[id] {
			return nil, fmt.Errorf("duplicate only entry !%s", id)
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

func (cfg Config) validate() error {
	if !configAbs(cfg.SVN) || !configAbs(cfg.SSH) || !configAbs(cfg.Identity) || !configAbs(cfg.KnownHosts) {
		return errors.New("svn, ssh, identity and known_hosts must be absolute")
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return errors.New("port must be 1-65535")
	}
	if len(cfg.Copies) == 0 {
		return errors.New("at least one working copy is required")
	}
	for id, spec := range cfg.Copies {
		if !configAbs(spec.Path) || strings.TrimSpace(spec.URL) == "" {
			return fmt.Errorf("working copy %s needs an absolute path and a url", id)
		}
	}
	for _, id := range cfg.Only {
		if _, ok := cfg.Copies[id]; !ok {
			return fmt.Errorf("only names unknown working copy %s", id)
		}
	}
	return nil
}

// configAbs accepts OpenBSD absolute paths (/var/...) even when the unit
// test runs on Windows, where filepath.IsAbs would reject them.
func configAbs(path string) bool {
	return filepath.IsAbs(path) || strings.HasPrefix(path, "/")
}

func (cfg Config) allowed() map[string]workingCopySpec {
	out := map[string]workingCopySpec{}
	if len(cfg.Only) == 0 {
		for id, spec := range cfg.Copies {
			out[id] = spec
		}
		return out
	}
	for _, id := range cfg.Only {
		out[id] = cfg.Copies[id]
	}
	return out
}

func (cfg Config) selectCopy(id string) (workingCopySpec, error) {
	id = strings.TrimSpace(id)
	spec, ok := cfg.allowed()[id]
	if !ok {
		return workingCopySpec{}, fmt.Errorf("working copy %q is not allowed by config", id)
	}
	return spec, nil
}
