package serverconfig

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"filees/pkg/activation"
	"filees/pkg/onboarding"
	"filees/pkg/smtpsubmit"
	"golang.org/x/crypto/ssh"
)

const Schema = "filees.server-toolchain/v1"

type File struct {
	Schema               string           `json:"schema"`
	Root                 string           `json:"root"`
	OTPPepperFile        string           `json:"otp_pepper_file"`
	OperationTTL         string           `json:"operation_ttl"`
	OTPAttempts          int              `json:"otp_attempts"`
	ReversePortFirst     uint16           `json:"reverse_port_first"`
	ReversePortLast      uint16           `json:"reverse_port_last"`
	WorkerPrivateKeyFile string           `json:"worker_private_key_file,omitempty"`
	WorkerPublicKeyFile  string           `json:"worker_public_key_file,omitempty"`
	Activation           ActivationFile   `json:"activation,omitempty"`
	Repositories         RepositoryFile   `json:"repositories,omitempty"`
	Invitation           InvitationFile   `json:"invitation,omitempty"`
	SMTP                 SMTPFile         `json:"smtp"`
	PublicShares         PublicSharesFile `json:"public_shares,omitempty"`
}

type PublicSharesFile struct {
	Enabled                bool   `json:"enabled,omitempty"`
	BaseURL                string `json:"base_url,omitempty"`
	StateRoot              string `json:"state_root,omitempty"`
	FrostKeyFile           string `json:"frost_key_file,omitempty"`
	AuthorityStagingRoot   string `json:"authority_staging_root,omitempty"`
	BackchannelNetwork     string `json:"backchannel_network,omitempty"`
	BackchannelAddress     string `json:"backchannel_address,omitempty"`
	BackchannelSocketGroup string `json:"backchannel_socket_group,omitempty"`
	MaxLeafSize            int64  `json:"max_size,omitempty"`
	MaxChannelsPerRealm    int    `json:"max_channels_per_realm,omitempty"`
	PasswordRequired       bool   `json:"password_required,omitempty"`
}

// InvitationFile contains the public, pinned bootstrap profile that is
// embedded in each administrator-issued invitation.
type InvitationFile struct {
	ServerID      string `json:"server_id,omitempty"`
	ServerAddress string `json:"server_address,omitempty"`
	KnownHost     string `json:"known_host,omitempty"`
}

type ActivationFile struct {
	Root               string `json:"root"`
	SessionRoot        string `json:"session_root,omitempty"`
	AuthorizedKeysFile string `json:"authorized_keys_file"`
	AuthzFile          string `json:"authz_file"`
	ServiceWorkingCopy string `json:"service_working_copy"`
	ServiceRepository  string `json:"service_repository"`
	RepositoryName     string `json:"repository_name"`
	ClientEntryPath    string `json:"client_entry_path"`
	MobileEntryPath    string `json:"mobile_entry_path,omitempty"`
	SVNBinary          string `json:"svn_binary"`
	SVNServeBinary     string `json:"svnserve_binary"`
}
type RepositoryFile struct {
	Root                  string `json:"root,omitempty"`
	ResultsRoot           string `json:"results_root,omitempty"`
	DataAuthzFile         string `json:"data_authz_file,omitempty"`
	SVNAdminBinary        string `json:"svnadmin_binary,omitempty"`
	SVNLookBinary         string `json:"svnlook_binary,omitempty"`
	SVNDumpFilterBinary   string `json:"svndumpfilter_binary,omitempty"`
	URLPrefix             string `json:"url_prefix,omitempty"`
	DeletionArchiveRoot   string `json:"deletion_archive_root,omitempty"`
	DeletionRetentionDays *int   `json:"deletion_retention_days,omitempty"`
	RecoveryAdminContact  string `json:"recovery_admin_contact"`
	DataErasureMaxDays    *int   `json:"data_erasure_max_days,omitempty"`
}

// EffectiveSVNLookBinary returns the configured svnlook path, or — since
// svnlook always ships alongside svnadmin in the same SVN distribution — the
// sibling of SVNAdminBinary if left unconfigured, so existing server.json
// files keep working without an admin edit.
func (repository RepositoryFile) EffectiveSVNLookBinary() string {
	if repository.SVNLookBinary != "" {
		return repository.SVNLookBinary
	}
	if repository.SVNAdminBinary == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(repository.SVNAdminBinary), "svnlook")
}

// EffectiveSVNDumpFilterBinary mirrors EffectiveSVNLookBinary for svndumpfilter.
func (repository RepositoryFile) EffectiveSVNDumpFilterBinary() string {
	if repository.SVNDumpFilterBinary != "" {
		return repository.SVNDumpFilterBinary
	}
	if repository.SVNAdminBinary == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(repository.SVNAdminBinary), "svndumpfilter")
}

func (repository RepositoryFile) EffectiveDeletionRetentionDays() int {
	if repository.DeletionRetentionDays == nil {
		return 30
	}
	return *repository.DeletionRetentionDays
}

func (repository RepositoryFile) EffectiveDataErasureMaxDays() int {
	if repository.DataErasureMaxDays == nil {
		return 90
	}
	return *repository.DataErasureMaxDays
}

type SMTPFile struct {
	Address         string `json:"address"`
	ServerName      string `json:"server_name,omitempty"`
	ClientName      string `json:"client_name,omitempty"`
	From            string `json:"from"`
	MessageIDDomain string `json:"message_id_domain"`
	Username        string `json:"username,omitempty"`
	PasswordFile    string `json:"password_file,omitempty"`
	TLS             string `json:"tls"`
	CAFile          string `json:"ca_file,omitempty"`
	ConnectTimeout  string `json:"connect_timeout,omitempty"`
	CommandTimeout  string `json:"command_timeout,omitempty"`
}

type Config struct {
	Path                 string
	Root                 string
	OTPPepperFile        string
	Onboarding           onboarding.Options
	SMTP                 smtpsubmit.Config
	SMTPFrom             string
	MessageIDDomain      string
	SMTPPasswordFile     string
	SMTPCAFile           string
	WorkerPrivateKeyFile string
	WorkerPublicKeyFile  string
	WorkerPublicKey      string
	WorkerSigner         ssh.Signer
	Activation           activation.Config
	Repositories         RepositoryFile
	Invitation           InvitationFile
	PublicShares         PublicSharesFile
	PublicShareFrostKey  []byte
}

type Secrets uint8

const (
	SecretOTP Secrets = 1 << iota
	SecretSMTP
	SecretWorker
	SecretWorkerPublic
	SecretActivation
	SecretPublicShares
)

// Load retains the original full onboarding configuration contract.
func Load(path string) (Config, error) { return LoadFor(path, SecretOTP) }

func LoadMail(path string) (Config, error) { return LoadFor(path, SecretSMTP) }

// LoadFor reads only secrets required by the selected tool. Paths and all
// non-secret configuration are still validated strictly.
func LoadFor(path string, secrets Secrets) (Config, error) { return load(path, secrets) }

func load(path string, secrets Secrets) (Config, error) {
	if !filepath.IsAbs(path) {
		return Config{}, errors.New("server toolchain config path must be absolute")
	}
	if err := requireRegularNotWritable(path); err != nil {
		return Config{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	if len(raw) > 64*1024 {
		return Config{}, errors.New("server toolchain config exceeds 64 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var file File
	if err := decoder.Decode(&file); err != nil {
		return Config{}, fmt.Errorf("decode server toolchain config: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Config{}, errors.New("decode server toolchain config: trailing JSON value")
	}
	if file.Schema != Schema {
		return Config{}, fmt.Errorf("unsupported server toolchain schema %q", file.Schema)
	}
	for label, value := range map[string]string{"root": file.Root, "otp_pepper_file": file.OTPPepperFile} {
		if !filepath.IsAbs(value) {
			return Config{}, fmt.Errorf("%s must be absolute", label)
		}
	}
	if file.WorkerPrivateKeyFile != "" && !filepath.IsAbs(file.WorkerPrivateKeyFile) {
		return Config{}, errors.New("worker_private_key_file must be absolute")
	}
	if file.WorkerPublicKeyFile != "" && !filepath.IsAbs(file.WorkerPublicKeyFile) {
		return Config{}, errors.New("worker_public_key_file must be absolute")
	}
	// Validated unconditionally rather than only when partly filled in. The
	// block carries the pinned host key that makes an invitation trustworthy,
	// and a server without it cannot onboard anyone - so accepting its absence
	// let a config pass every check and then fail at `ticket create`, which is
	// the worst moment to discover it. An invitation with a bad profile was
	// never issuable (EncodeInvitation validates), so this moves the refusal
	// earlier rather than closing a hole.
	probe := onboarding.Invitation{Schema: onboarding.InvitationSchema, Token: strings.Repeat("A", 43), ServerID: file.Invitation.ServerID, ServerAddress: file.Invitation.ServerAddress, KnownHost: file.Invitation.KnownHost}
	if err := probe.Validate(); err != nil {
		return Config{}, fmt.Errorf("invitation profile: %w (the invitation block is required; it carries the pinned host key)", err)
	}
	activationConfig := activation.Config{
		Root: file.Activation.Root, SessionRoot: file.Activation.SessionRoot, AuthorizedKeysFile: file.Activation.AuthorizedKeysFile,
		AuthzFile: file.Activation.AuthzFile, DataAuthzFile: file.Repositories.DataAuthzFile, ServiceWorkingCopy: file.Activation.ServiceWorkingCopy,
		ServiceRepository: file.Activation.ServiceRepository,
		RepositoryName:    file.Activation.RepositoryName, ClientEntryPath: file.Activation.ClientEntryPath,
		MobileEntryPath: file.Activation.MobileEntryPath,
		SVNBinary:       file.Activation.SVNBinary, SVNServeBinary: file.Activation.SVNServeBinary,
	}
	if secrets&SecretActivation != 0 {
		if _, err := activation.New(activationConfig, nil); err != nil {
			return Config{}, err
		}
		if err := requireRegularNotWritable(activationConfig.SVNBinary); err != nil {
			return Config{}, fmt.Errorf("activation svn_binary: %w", err)
		}
		if err := requireRegularNotWritable(activationConfig.SVNServeBinary); err != nil {
			return Config{}, fmt.Errorf("activation svnserve_binary: %w", err)
		}
	}
	workerPublicKey := ""
	if secrets&SecretWorkerPublic != 0 {
		if file.WorkerPublicKeyFile == "" {
			return Config{}, errors.New("worker_public_key_file is required for onboarding")
		}
		if err := requireRegularNotWritable(file.WorkerPublicKeyFile); err != nil {
			return Config{}, fmt.Errorf("worker public key: %w", err)
		}
		raw, err := os.ReadFile(file.WorkerPublicKeyFile)
		if err != nil || len(raw) > 16*1024 {
			return Config{}, errors.New("worker public key cannot be read or is oversized")
		}
		key, _, options, rest, err := ssh.ParseAuthorizedKey(bytes.TrimSpace(raw))
		if err != nil || key.Type() != ssh.KeyAlgoED25519 || len(options) != 0 || len(bytes.TrimSpace(rest)) != 0 {
			return Config{}, errors.New("worker public key must contain one Ed25519 authorized key")
		}
		workerPublicKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
	}
	var workerSigner ssh.Signer
	if secrets&SecretWorker != 0 {
		if file.WorkerPrivateKeyFile == "" {
			return Config{}, errors.New("worker_private_key_file is required for deploy worker")
		}
		workerRaw, err := readPrivateSecret(file.WorkerPrivateKeyFile)
		if err != nil {
			return Config{}, fmt.Errorf("worker private key: %w", err)
		}
		workerSigner, err = ssh.ParsePrivateKey(workerRaw)
		if err != nil || workerSigner.PublicKey().Type() != ssh.KeyAlgoED25519 {
			return Config{}, errors.New("worker private key must be an unencrypted Ed25519 OpenSSH key")
		}
	}
	ttl, err := time.ParseDuration(file.OperationTTL)
	if err != nil || ttl <= 0 {
		return Config{}, errors.New("operation_ttl must be a positive duration")
	}
	var pepper []byte
	if secrets&SecretOTP != 0 {
		pepperRaw, err := readPrivateSecret(file.OTPPepperFile)
		if err != nil {
			return Config{}, fmt.Errorf("OTP pepper: %w", err)
		}
		pepper, err = base64.StdEncoding.DecodeString(string(pepperRaw))
		if err != nil || len(pepper) < 32 {
			return Config{}, errors.New("OTP pepper file must contain base64 for at least 32 bytes")
		}
	}
	var publicShareFrostKey []byte
	if file.PublicShares.Enabled && secrets&SecretPublicShares != 0 {
		keyRaw, err := readPrivateSecret(file.PublicShares.FrostKeyFile)
		if err != nil {
			return Config{}, fmt.Errorf("public share frost key: %w", err)
		}
		publicShareFrostKey, err = base64.StdEncoding.DecodeString(string(keyRaw))
		if err != nil || len(publicShareFrostKey) < 32 {
			return Config{}, errors.New("public share frost key must contain base64 for at least 32 bytes")
		}
	}
	connectTimeout, err := optionalDuration(file.SMTP.ConnectTimeout, 10*time.Second)
	if err != nil {
		return Config{}, fmt.Errorf("SMTP connect_timeout: %w", err)
	}
	commandTimeout, err := optionalDuration(file.SMTP.CommandTimeout, 30*time.Second)
	if err != nil {
		return Config{}, fmt.Errorf("SMTP command_timeout: %w", err)
	}
	from, err := onboarding.CanonicalEmail(file.SMTP.From)
	if err != nil {
		return Config{}, fmt.Errorf("SMTP from: %w", err)
	}
	password := ""
	if file.SMTP.Username != "" || file.SMTP.PasswordFile != "" {
		if file.SMTP.Username == "" || !filepath.IsAbs(file.SMTP.PasswordFile) {
			return Config{}, errors.New("SMTP username requires an absolute password_file and vice versa")
		}
		if secrets&SecretSMTP != 0 {
			secret, err := readPrivateSecret(file.SMTP.PasswordFile)
			if err != nil {
				return Config{}, fmt.Errorf("SMTP password: %w", err)
			}
			password = string(secret)
		}
	}
	var pool *x509.CertPool
	if file.SMTP.CAFile != "" {
		if !filepath.IsAbs(file.SMTP.CAFile) {
			return Config{}, errors.New("SMTP ca_file must be absolute")
		}
		if secrets&SecretSMTP != 0 {
			pool, err = x509.SystemCertPool()
			if err != nil || pool == nil {
				pool = x509.NewCertPool()
			}
			pemBytes, err := os.ReadFile(file.SMTP.CAFile)
			if err != nil {
				return Config{}, fmt.Errorf("SMTP CA: %w", err)
			}
			if !pool.AppendCertsFromPEM(pemBytes) {
				return Config{}, errors.New("SMTP ca_file contains no certificates")
			}
		}
	} else if secrets&SecretSMTP != 0 {
		pool, err = x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
	}
	if strings.TrimSpace(file.Repositories.RecoveryAdminContact) == "" {
		file.Repositories.RecoveryAdminContact = file.SMTP.From
	}
	config := Config{
		Path: path, Root: filepath.Clean(file.Root), OTPPepperFile: file.OTPPepperFile, SMTPFrom: from,
		MessageIDDomain:  strings.ToLower(strings.TrimSpace(file.SMTP.MessageIDDomain)),
		SMTPPasswordFile: file.SMTP.PasswordFile, SMTPCAFile: file.SMTP.CAFile,
		WorkerPrivateKeyFile: file.WorkerPrivateKeyFile, WorkerSigner: workerSigner,
		WorkerPublicKeyFile: file.WorkerPublicKeyFile, WorkerPublicKey: workerPublicKey,
		Invitation:   file.Invitation,
		PublicShares: file.PublicShares, PublicShareFrostKey: publicShareFrostKey,
		Activation:   activationConfig,
		Repositories: file.Repositories,
		Onboarding:   onboarding.Options{OTPPepper: pepper, OperationTTL: ttl, OTPAttempts: file.OTPAttempts, ReversePortFirst: file.ReversePortFirst, ReversePortLast: file.ReversePortLast},
		SMTP:         smtpsubmit.Config{Address: file.SMTP.Address, ServerName: file.SMTP.ServerName, ClientName: file.SMTP.ClientName, Username: file.SMTP.Username, Password: password, TLSMode: smtpsubmit.TLSMode(file.SMTP.TLS), RootCAs: pool, ConnectTimeout: connectTimeout, CommandTimeout: commandTimeout},
	}
	if config.Repositories.EffectiveDeletionRetentionDays() < 0 {
		return Config{}, errors.New("repositories deletion_retention_days cannot be negative")
	}
	if days := config.Repositories.EffectiveDataErasureMaxDays(); days <= 0 || days > 3650 {
		return Config{}, errors.New("repositories data_erasure_max_days must be between 1 and 3650")
	}
	if config.Repositories.DeletionArchiveRoot != "" && !filepath.IsAbs(config.Repositories.DeletionArchiveRoot) {
		return Config{}, errors.New("repositories deletion_archive_root must be absolute")
	}
	if _, err := onboarding.CanonicalEmail(config.Repositories.RecoveryAdminContact); err != nil {
		return Config{}, errors.New("repositories recovery_admin_contact must be a plain mailbox address")
	}
	if config.MessageIDDomain == "" || strings.ContainsAny(config.MessageIDDomain, "@<>\r\n \t") {
		return Config{}, errors.New("SMTP message_id_domain is required")
	}
	smtpForValidation := config.SMTP
	if smtpForValidation.Username != "" && secrets&SecretSMTP == 0 {
		// Validate the non-secret AUTH shape without opening the password file
		// for tools which never submit mail.
		smtpForValidation.Password = "configured-out-of-process"
	}
	if err := smtpsubmit.ValidateConfig(smtpForValidation); err != nil {
		return Config{}, fmt.Errorf("SMTP config: %w", err)
	}
	if err := validatePublicShares(file.PublicShares, file.Repositories.ResultsRoot); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (p PublicSharesFile) EffectiveStateRoot(resultsRoot string) string {
	if p.StateRoot != "" {
		return filepath.Clean(p.StateRoot)
	}
	return filepath.Join(resultsRoot, "public-shares")
}

func (p PublicSharesFile) EffectiveAuthorityStagingRoot() string {
	if p.AuthorityStagingRoot != "" {
		return filepath.Clean(p.AuthorityStagingRoot)
	}
	return "/var/tmp/filees-public-share-authority"
}

func (p PublicSharesFile) EffectiveMaxLeafSize() int64 {
	if p.MaxLeafSize != 0 {
		return p.MaxLeafSize
	}
	return 1 << 30
}

func (p PublicSharesFile) EffectiveMaxChannelsPerRealm() int {
	if p.MaxChannelsPerRealm != 0 {
		return p.MaxChannelsPerRealm
	}
	return 128
}

func validatePublicShares(p PublicSharesFile, resultsRoot string) error {
	if !p.Enabled {
		return nil
	}
	base, err := url.Parse(p.BaseURL)
	if err != nil || base.Scheme != "https" || base.Host == "" || (base.Path != "" && base.Path != "/") || base.RawQuery != "" || base.Fragment != "" {
		return errors.New("public_shares base_url must be an HTTPS origin")
	}
	if !filepath.IsAbs(p.EffectiveStateRoot(resultsRoot)) || !filepath.IsAbs(p.FrostKeyFile) || !filepath.IsAbs(p.EffectiveAuthorityStagingRoot()) {
		return errors.New("public_shares state, frost key and staging paths must be absolute")
	}
	if p.EffectiveMaxLeafSize() <= 0 || p.EffectiveMaxLeafSize() > 1<<40 {
		return errors.New("public_shares max_size must be positive and at most 1 TiB")
	}
	if p.EffectiveMaxChannelsPerRealm() < 1 || p.EffectiveMaxChannelsPerRealm() > 100000 {
		return errors.New("public_shares max_channels_per_realm must be 1 to 100000")
	}
	if p.BackchannelNetwork != "unix" && p.BackchannelNetwork != "tcp" {
		return errors.New("public_shares backchannel_network must be unix or tcp")
	}
	if p.BackchannelNetwork == "unix" {
		if !filepath.IsAbs(p.BackchannelAddress) {
			return errors.New("public_shares unix backchannel address must be absolute")
		}
		return nil
	}
	if p.BackchannelSocketGroup != "" {
		return errors.New("public_shares backchannel_socket_group is only valid for unix")
	}
	host, port, err := net.SplitHostPort(p.BackchannelAddress)
	if err != nil {
		return errors.New("public_shares tcp backchannel address is invalid")
	}
	portNumber, err := strconv.Atoi(port)
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || !ip.IsLoopback() || err != nil || portNumber < 1 || portNumber > 65535 {
		return errors.New("public_shares tcp backchannel must bind loopback")
	}
	return nil
}

func readPrivateSecret(path string) ([]byte, error) {
	if err := requirePrivate(path); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	raw = bytes.TrimSuffix(raw, []byte("\n"))
	raw = bytes.TrimSuffix(raw, []byte("\r"))
	if len(raw) == 0 || len(raw) > 4096 || bytes.IndexByte(raw, 0) >= 0 {
		return nil, errors.New("secret file is empty, oversized or contains NUL")
	}
	return raw, nil
}

func requirePrivate(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("secret file %s must be regular and mode 0600", path)
	}
	return nil
}

func requireRegularNotWritable(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("config file %s must not be writable by group or others", path)
	}
	return nil
}

func optionalDuration(value string, fallback time.Duration) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, errors.New("must be a positive duration")
	}
	return duration, nil
}
