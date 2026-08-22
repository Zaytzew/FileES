// Package linkservice wires the credential-free public FastCGI process.
package linkservice

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"filees/public-shares/backchannel"
	"filees/public-shares/cache"
	"filees/public-shares/intake"
	"filees/public-shares/web"
)

const ConfigSchema = "filees.public-links/v1"

type Endpoint struct {
	Network string `json:"network"`
	Address string `json:"address"`
}

type FastCGIEndpoint struct {
	Endpoint
	SocketGroup string `json:"socket_group,omitempty"`
}

type CacheConfig struct {
	Enabled bool   `json:"enabled"`
	Root    string `json:"root,omitempty"`
	TTL     string `json:"ttl,omitempty"`
	MaxSize int64  `json:"max_size,omitempty"`
}

type BundleConfig struct {
	MaxFiles int   `json:"max_files,omitempty"`
	MaxSize  int64 `json:"max_size,omitempty"`
}

type Config struct {
	Schema        string          `json:"schema"`
	FastCGI       FastCGIEndpoint `json:"fastcgi"`
	Backchannel   Endpoint        `json:"backchannel"`
	VisitKeyFile  string          `json:"visit_key_file"`
	Cache         CacheConfig     `json:"cache"`
	Bundle        BundleConfig    `json:"bundle,omitempty"`
	IntakeRoot    string          `json:"intake_root,omitempty"`
	MaxUploadSize int64           `json:"max_upload_size,omitempty"`
}

type Runtime struct {
	Config         Config
	VisitKey       []byte
	CacheTTL       time.Duration
	BundleMaxFiles int
	BundleMaxSize  int64
	Intake         *intake.Store
}

func Load(path string) (Runtime, error) {
	if !filepath.IsAbs(path) {
		return Runtime{}, errors.New("public links config path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
		return Runtime{}, errors.New("public links config must be a regular file not writable by group or others")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Runtime{}, err
	}
	if len(raw) > 64*1024 {
		return Runtime{}, errors.New("public links config is too large")
	}
	var config Config
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Runtime{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Runtime{}, errors.New("public links config contains trailing JSON")
	}
	if config.Schema != ConfigSchema {
		return Runtime{}, errors.New("public links config schema is invalid")
	}
	if err := validateEndpoint(config.FastCGI.Endpoint, true); err != nil {
		return Runtime{}, fmt.Errorf("fastcgi: %w", err)
	}
	if err := validateEndpoint(config.Backchannel, true); err != nil {
		return Runtime{}, fmt.Errorf("backchannel: %w", err)
	}
	if config.FastCGI.Network == "tcp" && config.FastCGI.SocketGroup != "" {
		return Runtime{}, errors.New("fastcgi socket_group is only valid for unix")
	}
	if !filepath.IsAbs(config.VisitKeyFile) {
		return Runtime{}, errors.New("visit_key_file must be absolute")
	}
	key, err := readKey(config.VisitKeyFile)
	if err != nil {
		return Runtime{}, fmt.Errorf("visit key: %w", err)
	}
	ttl := 12 * time.Hour
	if config.Cache.Enabled {
		if !filepath.IsAbs(config.Cache.Root) || config.Cache.MaxSize <= 0 {
			return Runtime{}, errors.New("enabled cache requires absolute root and positive max_size")
		}
		if config.Cache.TTL != "" {
			ttl, err = time.ParseDuration(config.Cache.TTL)
			if err != nil || ttl <= 0 || ttl > 24*time.Hour {
				return Runtime{}, errors.New("cache ttl must be positive and at most 24h")
			}
		}
		if err := os.MkdirAll(config.Cache.Root, 0700); err != nil {
			return Runtime{}, fmt.Errorf("cache root: %w", err)
		}
	}
	bundleFiles, bundleSize := 0, int64(0)
	if config.Cache.Enabled {
		bundleFiles, bundleSize = config.Bundle.MaxFiles, config.Bundle.MaxSize
		if bundleFiles == 0 {
			bundleFiles = 512
		}
		if bundleSize == 0 {
			bundleSize = 1 << 30
			if config.Cache.MaxSize < bundleSize {
				bundleSize = config.Cache.MaxSize
			}
		}
		if bundleFiles < 1 || bundleFiles > 4096 || bundleSize < 1 || bundleSize > 1<<40 || bundleSize > config.Cache.MaxSize {
			return Runtime{}, errors.New("bundle limits must fit 1..4096 files and the enabled cache capacity")
		}
	} else if config.Bundle.MaxFiles != 0 || config.Bundle.MaxSize != 0 {
		return Runtime{}, errors.New("bundle downloads require the public leaf cache")
	}
	var quarantine *intake.Store
	if config.IntakeRoot != "" {
		if !filepath.IsAbs(config.IntakeRoot) {
			return Runtime{}, errors.New("intake_root must be absolute")
		}
		if config.Cache.Enabled && filepath.Clean(config.IntakeRoot) == filepath.Clean(config.Cache.Root) {
			return Runtime{}, errors.New("intake_root must not share the public leaf cache")
		}
		maxUpload := config.MaxUploadSize
		if maxUpload == 0 {
			maxUpload = 1 << 30
		}
		if maxUpload < 1 || maxUpload > 1<<40 {
			return Runtime{}, errors.New("max_upload_size is out of range")
		}
		if err := os.MkdirAll(config.IntakeRoot, 0700); err != nil {
			return Runtime{}, fmt.Errorf("intake root: %w", err)
		}
		quarantine = &intake.Store{Root: filepath.Clean(config.IntakeRoot), MaxBytes: maxUpload}
	} else if config.MaxUploadSize != 0 {
		return Runtime{}, errors.New("max_upload_size requires intake_root")
	}
	return Runtime{Config: config, VisitKey: key, CacheTTL: ttl, BundleMaxFiles: bundleFiles, BundleMaxSize: bundleSize, Intake: quarantine}, nil
}

func (r Runtime) Handler() http.Handler {
	transport := &http.Transport{DisableCompression: true, MaxIdleConns: 8, MaxIdleConnsPerHost: 8, IdleConnTimeout: 30 * time.Second}
	endpoint := r.Config.Backchannel
	transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 15 * time.Second}).DialContext(ctx, endpoint.Network, endpoint.Address)
	}
	client := backchannel.Client{BaseURL: "http://filees-authority", HTTP: &http.Client{Transport: transport, Timeout: 30 * time.Minute}}
	var store *cache.Store
	if r.Config.Cache.Enabled {
		store = &cache.Store{Config: cache.Config{Root: r.Config.Cache.Root, TTL: r.CacheTTL, MaxSize: r.Config.Cache.MaxSize}}
	}
	maxUpload := int64(0)
	if r.Intake != nil {
		maxUpload = r.Intake.MaxBytes
	}
	return web.Handler{Backend: client, Cache: store, Fetches: &web.FetchCoordinator{}, VisitKey: r.VisitKey, MaxBundleFiles: r.BundleMaxFiles, MaxBundleSize: r.BundleMaxSize, BundleSlots: make(chan struct{}, 1), Intake: r.Intake, MaxUploadBytes: maxUpload}
}

func (r Runtime) ListenFastCGI() (net.Listener, func(), error) {
	endpoint := r.Config.FastCGI
	if endpoint.Network == "tcp" {
		listener, err := net.Listen("tcp", endpoint.Address)
		return listener, func() {}, err
	}
	if err := os.MkdirAll(filepath.Dir(endpoint.Address), 0750); err != nil {
		return nil, nil, err
	}
	if info, err := os.Lstat(endpoint.Address); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, nil, errors.New("fastcgi address exists and is not a socket")
		}
		if err := os.Remove(endpoint.Address); err != nil {
			return nil, nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	listener, err := net.Listen("unix", endpoint.Address)
	if err != nil {
		return nil, nil, err
	}
	if err := os.Chmod(endpoint.Address, 0660); err != nil {
		listener.Close()
		os.Remove(endpoint.Address)
		return nil, nil, err
	}
	if endpoint.SocketGroup != "" {
		group, err := user.LookupGroup(endpoint.SocketGroup)
		if err != nil {
			listener.Close()
			os.Remove(endpoint.Address)
			return nil, nil, err
		}
		gid, err := strconv.Atoi(group.Gid)
		if err != nil || os.Chown(endpoint.Address, -1, gid) != nil {
			listener.Close()
			os.Remove(endpoint.Address)
			return nil, nil, errors.New("cannot assign FastCGI socket group")
		}
	}
	created, _ := os.Lstat(endpoint.Address)
	cleanup := func() {
		listener.Close()
		if current, err := os.Lstat(endpoint.Address); err == nil && created != nil && os.SameFile(created, current) {
			_ = os.Remove(endpoint.Address)
		}
	}
	return listener, cleanup, nil
}

func (r Runtime) SandboxPaths() []string {
	paths := []string{}
	if r.Config.Cache.Enabled {
		paths = append(paths, filepath.Clean(r.Config.Cache.Root))
	}
	if r.Config.FastCGI.Network == "unix" {
		paths = append(paths, r.Config.FastCGI.Address)
	}
	if r.Config.Backchannel.Network == "unix" {
		paths = append(paths, r.Config.Backchannel.Address)
	}
	if r.Config.IntakeRoot != "" {
		paths = append(paths, filepath.Clean(r.Config.IntakeRoot))
	}
	return paths
}

func validateEndpoint(endpoint Endpoint, loopbackTCP bool) error {
	if endpoint.Network != "unix" && endpoint.Network != "tcp" {
		return errors.New("network must be unix or tcp")
	}
	if endpoint.Network == "unix" {
		if !filepath.IsAbs(endpoint.Address) {
			return errors.New("unix address must be absolute")
		}
		return nil
	}
	host, port, err := net.SplitHostPort(endpoint.Address)
	if err != nil {
		return errors.New("tcp address is invalid")
	}
	parsedPort, err := strconv.Atoi(port)
	if err != nil || parsedPort < 1 || parsedPort > 65535 {
		return errors.New("tcp port is invalid")
	}
	if loopbackTCP {
		ip := net.ParseIP(strings.Trim(host, "[]"))
		if ip == nil || !ip.IsLoopback() {
			return errors.New("backchannel tcp address must be loopback")
		}
	}
	return nil
}

func readKey(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("key file must be regular mode 0600")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(key) < 32 {
		return nil, errors.New("key file must contain base64 for at least 32 bytes")
	}
	return key, nil
}
