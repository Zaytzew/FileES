package activation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"filees/pkg/onboarding"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

const (
	RecordSchema  = "filees.activation-record/v1"
	ReceiptSchema = "filees.possession-receipt/v1"
	FormatSchema  = "filees.service-repository/v1"
	RealmSchema   = "filees.realm/v1"
	ClientSchema  = "filees.client-instance/v1"
	ViewSchema    = "filees.client-view/v1"
	AuditSchema   = "filees.service-audit/v1"
)

type Config struct {
	Root string
	// SessionRoot is private runtime state for per-SSH-session leases. It
	// must be outside every SVN working copy and repository. Empty is kept
	// only as a migration fallback to Root/sessions for installations created
	// before supervised sessions existed.
	SessionRoot        string
	AuthorizedKeysFile string
	AuthzFile          string
	ServiceWorkingCopy string
	ServiceRepository  string
	RepositoryName     string
	ClientEntryPath    string
	// MobileEntryPath is the forced-command binary bound to Record.Kind ==
	// onboarding.KindMobile installations, alongside ClientEntryPath for
	// everything else. See renderAccessLocked.
	MobileEntryPath string
	SVNBinary       string
	SVNServeBinary  string
}

type Record struct {
	Schema          string `json:"schema"`
	OperationID     string `json:"operation_id"`
	DeployRequestID string `json:"deploy_request_id"`
	ClientID        string `json:"client_id"`
	RealmID         string `json:"realm_id"`
	// Kind mirrors onboarding.Policy.Kind. Empty (the zero value, including
	// every record persisted before this field existed) means
	// onboarding.KindDesktop.
	Kind                    string     `json:"kind,omitempty"`
	// Repositories carries the mobile pairing initiator's own repository
	// grants (Kind == onboarding.KindMobile only), consumed at publish time
	// by mobileRepositoryEntries to seed the new client's view.json.
	Repositories            []onboarding.MobileRepositoryGrant `json:"repositories,omitempty"`
	InstallationPublicKey   string     `json:"installation_public_key"`
	InstallationFingerprint string     `json:"installation_fingerprint"`
	State                   string     `json:"state"`
	CreatedAt               time.Time  `json:"created_at"`
	ExpiresAt               time.Time  `json:"expires_at"`
	ActivatedAt             *time.Time `json:"activated_at,omitempty"`
	ServiceRevision         int64      `json:"service_revision,omitempty"`
	RevokedAt               *time.Time `json:"revoked_at,omitempty"`
	RevokeReason            string     `json:"revoke_reason,omitempty"`
}

type Receipt struct {
	Schema      string    `json:"schema"`
	OperationID string    `json:"operation_id"`
	ClientID    string    `json:"client_id"`
	At          time.Time `json:"at"`
}

type Realm struct {
	Schema    string    `json:"schema"`
	RealmID   string    `json:"realm_id"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
}

type CommandRunner interface {
	Output(context.Context, string, ...string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) Output(ctx context.Context, command string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, command, args...).CombinedOutput()
}

type Manager struct {
	config Config
	runner CommandRunner
	now    func() time.Time
}

func New(config Config, runner CommandRunner) (*Manager, error) {
	for label, path := range map[string]string{
		"root": config.Root, "authorized_keys_file": config.AuthorizedKeysFile,
		"authz_file": config.AuthzFile, "service_working_copy": config.ServiceWorkingCopy,
		"service_repository": config.ServiceRepository, "client_entry_path": config.ClientEntryPath,
		"svn_binary": config.SVNBinary, "svnserve_binary": config.SVNServeBinary,
	} {
		if !filepath.IsAbs(path) {
			return nil, fmt.Errorf("activation %s must be absolute", label)
		}
	}
	if config.SessionRoot != "" && !filepath.IsAbs(config.SessionRoot) {
		return nil, errors.New("activation session_root must be absolute")
	}
	if strings.TrimSpace(config.RepositoryName) == "" || strings.ContainsAny(config.RepositoryName, "/:\r\n[]") {
		return nil, errors.New("activation repository_name is invalid")
	}
	if strings.ContainsAny(config.ClientEntryPath, "\"' \t\r\n") {
		return nil, errors.New("activation client_entry_path cannot require shell quoting")
	}
	if config.MobileEntryPath != "" {
		if !filepath.IsAbs(config.MobileEntryPath) {
			return nil, errors.New("activation mobile_entry_path must be absolute")
		}
		if strings.ContainsAny(config.MobileEntryPath, "\"' \t\r\n") {
			return nil, errors.New("activation mobile_entry_path cannot require shell quoting")
		}
	}
	if runner == nil {
		runner = ExecRunner{}
	}
	return &Manager{config: config, runner: runner, now: time.Now}, nil
}

func (m *Manager) Stage(grant onboarding.ActivationGrant) error {
	canonical, err := validateGrant(grant)
	if err != nil {
		return err
	}
	return withFileLock(filepath.Join(m.config.Root, ".activation.lock"), func() error {
		if err := os.MkdirAll(m.recordsDir(), 0o700); err != nil {
			return err
		}
		if err := os.MkdirAll(m.proofsDir(), 0o700); err != nil {
			return err
		}
		if _, err := m.expireStagedLocked(m.now().UTC()); err != nil {
			return err
		}
		path := m.recordPath(grant.OperationID)
		existing, err := readRecord(path)
		if err == nil {
			if !sameGrant(existing, grant, canonical) {
				return errors.New("activation record conflicts with staged identity")
			}
			return m.renderAccessLocked()
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		now := m.now().UTC()
		record := Record{
			Schema: RecordSchema, OperationID: grant.OperationID, DeployRequestID: grant.DeployRequestID,
			ClientID: grant.ClientID, RealmID: grant.RealmID, Kind: grant.Kind, Repositories: grant.Repositories, InstallationPublicKey: canonical,
			InstallationFingerprint: grant.InstallationFingerprint, State: "staged", CreatedAt: now,
			ExpiresAt: grant.ExpiresAt.UTC(),
		}
		if err := atomicWriteJSON(path, record, 0o600); err != nil {
			return err
		}
		return m.renderAccessLocked()
	})
}

func (m *Manager) CleanupExpired() (int, error) {
	count := 0
	err := withFileLock(filepath.Join(m.config.Root, ".activation.lock"), func() error {
		var err error
		count, err = m.expireStagedLocked(m.now().UTC())
		if err != nil {
			return err
		}
		return m.renderAccessLocked()
	})
	return count, err
}

// CleanupOrphanedSessionLeases removes only leases whose private revoke FIFO
// has no reader. It shares the activation lock with ClaimSession, so a lease
// cannot be observed between publishing its directory and opening its FIFO.
// No age or PID heuristic participates in the decision.
func (m *Manager) CleanupOrphanedSessionLeases() (int, error) {
	count := 0
	err := withFileLock(filepath.Join(m.config.Root, ".activation.lock"), func() error {
		var err error
		count, err = cleanupOrphanedSessionLeases(m.sessionRoot())
		return err
	})
	return count, err
}

func (m *Manager) expireStagedLocked(now time.Time) (int, error) {
	records, err := m.recordsLocked()
	if err != nil {
		return 0, err
	}
	count := 0
	for _, record := range records {
		if record.State != "staged" || now.Before(record.ExpiresAt) {
			continue
		}
		record.State = "expired"
		if err := atomicWriteJSON(m.recordPath(record.OperationID), record, 0o600); err != nil {
			return count, err
		}
		_ = os.Remove(m.receiptPath(record.OperationID))
		count++
	}
	return count, nil
}

// RecordProof is called only by the per-key forced command after sshd has
// authenticated the staged installation key.
func (m *Manager) RecordProof(operationID, clientID string) error {
	if _, err := uuid.Parse(operationID); err != nil {
		return errors.New("proof operation_id must be a UUID")
	}
	if _, err := uuid.Parse(clientID); err != nil {
		return errors.New("proof client_id must be a UUID")
	}
	return withFileLock(filepath.Join(m.config.Root, ".activation.lock"), func() error {
		_, err := m.recordProofLocked(operationID, clientID)
		return err
	})
}

// ClaimSession records possession when required and creates a private
// supervised-session lease while holding the same lock as Revoke. Therefore a
// revoke either sees this lease and signals it, or wins the lock first and
// prevents the session from being admitted.
func (m *Manager) ClaimSession(operationID, clientID string) (*SessionLease, error) {
	if _, err := uuid.Parse(operationID); err != nil {
		return nil, errors.New("session operation_id must be a UUID")
	}
	if _, err := uuid.Parse(clientID); err != nil {
		return nil, errors.New("session client_id must be a UUID")
	}
	var lease *SessionLease
	err := withFileLock(filepath.Join(m.config.Root, ".activation.lock"), func() error {
		record, err := m.recordProofLocked(operationID, clientID)
		if err != nil {
			return err
		}
		if record.State != "active" && record.State != "staged" {
			return errors.New("session record is not active")
		}
		lease, err = createSessionLease(m.sessionRoot(), SessionMetadata{
			OperationID: operationID,
			ClientID:    clientID,
			RealmID:     record.RealmID,
			StartedAt:   m.now().UTC(),
		})
		return err
	})
	return lease, err
}

// SessionAllowed is deliberately read-only: record updates are atomic and a
// supervisor treats every error or non-live state as a fail-closed result.
func (m *Manager) SessionAllowed(operationID, clientID string) bool {
	record, err := readRecord(m.recordPath(operationID))
	if err != nil || record.ClientID != clientID {
		return false
	}
	if record.State == "active" {
		return true
	}
	return record.State == "staged" && m.now().UTC().Before(record.ExpiresAt)
}

func (m *Manager) recordProofLocked(operationID, clientID string) (Record, error) {
	record, err := readRecord(m.recordPath(operationID))
	if err != nil {
		return Record{}, err
	}
	now := m.now().UTC()
	if record.ClientID != clientID || record.State != "staged" || !now.Before(record.ExpiresAt) {
		if record.ClientID == clientID && record.State == "active" {
			return record, nil
		}
		return Record{}, errors.New("proof does not match one live staged or active client")
	}
	receipt := Receipt{Schema: ReceiptSchema, OperationID: operationID, ClientID: clientID, At: now}
	if err := atomicWriteJSON(m.receiptPath(operationID), receipt, 0o600); err != nil {
		return Record{}, err
	}
	return record, nil
}

func (m *Manager) sessionRoot() string {
	if m.config.SessionRoot != "" {
		return m.config.SessionRoot
	}
	return filepath.Join(m.config.Root, "sessions")
}

func (m *Manager) HasProof(grant onboarding.ActivationGrant) error {
	if !m.now().UTC().Before(grant.ExpiresAt) {
		return errors.New("activation expired before possession proof was finalized")
	}
	receipt, err := readReceipt(m.receiptPath(grant.OperationID))
	if err != nil {
		return err
	}
	if receipt.OperationID != grant.OperationID || receipt.ClientID != grant.ClientID || receipt.At.After(grant.ExpiresAt) {
		return errors.New("possession receipt does not match activation")
	}
	return nil
}

func (m *Manager) Publish(ctx context.Context, grant onboarding.ActivationGrant) (int64, error) {
	canonical, err := validateGrant(grant)
	if err != nil {
		return 0, err
	}
	var revision int64
	err = withFileLock(filepath.Join(m.config.Root, ".activation.lock"), func() error {
		record, err := readRecord(m.recordPath(grant.OperationID))
		if err != nil {
			return err
		}
		if record.State == "active" && sameGrant(record, grant, canonical) {
			revision = record.ServiceRevision
			return nil
		}
		if err := m.HasProof(grant); err != nil {
			return err
		}
		if record.State != "staged" || !sameGrant(record, grant, canonical) {
			return errors.New("activation record is not publishable")
		}
		receipt, err := readReceipt(m.receiptPath(grant.OperationID))
		if err != nil {
			return err
		}
		revision, err = m.publishServiceFiles(ctx, record, receipt.At.UTC())
		if err != nil {
			return err
		}
		activatedAt := receipt.At.UTC()
		record.State, record.ActivatedAt, record.ServiceRevision = "active", &activatedAt, revision
		if err := atomicWriteJSON(m.recordPath(grant.OperationID), record, 0o600); err != nil {
			return err
		}
		return m.renderAccessLocked()
	})
	return revision, err
}

// RevokeRealm first moves every selected client to revoking under one
// activation lock. That closes the admission barrier for all of the realm
// before publishing individual service revisions or notifying live leases.
func (m *Manager) RevokeRealm(ctx context.Context, realmID, reason string) ([]string, error) {
	if _, err := uuid.Parse(realmID); err != nil {
		return nil, errors.New("revoke realm_id must be a UUID")
	}
	reason, err := validateRevokeReason(reason)
	if err != nil {
		return nil, err
	}
	var targets []string
	err = withFileLock(filepath.Join(m.config.Root, ".activation.lock"), func() error {
		records, err := m.recordsLocked()
		if err != nil {
			return err
		}
		for _, record := range records {
			if record.RealmID != realmID || record.State == "revoked" {
				continue
			}
			if record.State == "staged" || record.State == "active" {
				now := m.now().UTC()
				record.State, record.RevokedAt, record.RevokeReason = "revoking", &now, reason
				if err := atomicWriteJSON(m.recordPath(record.OperationID), record, 0o600); err != nil {
					return err
				}
			} else if record.State != "revoking" || record.RevokeReason != reason || record.RevokedAt == nil {
				return fmt.Errorf("client %s is not revocable from its current state", record.ClientID)
			}
			targets = append(targets, record.ClientID)
		}
		return m.renderAccessLocked()
	})
	if err != nil {
		return nil, err
	}
	for _, clientID := range targets {
		_ = signalSessionLeases(m.sessionRoot(), clientID, "")
	}
	var revoked []string
	for _, clientID := range targets {
		if _, err := m.Revoke(ctx, clientID, reason); err != nil {
			return revoked, fmt.Errorf("revoke client %s: %w", clientID, err)
		}
		revoked = append(revoked, clientID)
	}
	return revoked, nil
}

func (m *Manager) Revoke(ctx context.Context, clientID, reason string) (int64, error) {
	if _, err := uuid.Parse(clientID); err != nil {
		return 0, errors.New("revoke client_id must be a UUID")
	}
	reason, err := validateRevokeReason(reason)
	if err != nil {
		return 0, err
	}
	var revision int64
	signalLease := false
	err = withFileLock(filepath.Join(m.config.Root, ".activation.lock"), func() error {
		records, err := m.recordsLocked()
		if err != nil {
			return err
		}
		var selected *Record
		for i := range records {
			if records[i].ClientID == clientID {
				if selected != nil {
					return errors.New("multiple activation records for client")
				}
				selected = &records[i]
			}
		}
		if selected == nil {
			return os.ErrNotExist
		}
		record := *selected
		if record.State == "revoked" {
			if record.RevokeReason != reason {
				return errors.New("client was already revoked for a different reason")
			}
			revision = record.ServiceRevision
			signalLease = true
			return nil
		}
		if record.State == "staged" || record.State == "active" {
			now := m.now().UTC()
			record.State, record.RevokedAt, record.RevokeReason = "revoking", &now, reason
			if err := atomicWriteJSON(m.recordPath(record.OperationID), record, 0o600); err != nil {
				return err
			}
		} else if record.State != "revoking" || record.RevokeReason != reason || record.RevokedAt == nil {
			return errors.New("client is not revocable from its current state")
		}
		// Runtime access is removed (or re-removed after a crash) before the
		// public record is changed.
		if err := m.renderAccessLocked(); err != nil {
			return err
		}
		revision, err = m.publishRevocation(ctx, record)
		if err != nil {
			return err
		}
		record.State, record.ServiceRevision = "revoked", revision
		if err := atomicWriteJSON(m.recordPath(record.OperationID), record, 0o600); err != nil {
			return err
		}
		signalLease = true
		return m.renderAccessLocked()
	})
	if err == nil && signalLease {
		// The durable state transition is the security boundary. FIFO delivery
		// only reduces latency; a failed/open-less lease is reconciled by the
		// supervisor's one-second fail-closed poll.
		_ = signalSessionLeases(m.sessionRoot(), clientID, "")
	}
	return revision, err
}

func validateRevokeReason(reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 200 || strings.ContainsAny(reason, "\r\n") {
		return "", errors.New("revoke reason must be 1..200 characters on one line")
	}
	return reason, nil
}

func (m *Manager) publishRevocation(ctx context.Context, record Record) (int64, error) {
	clientPath := filepath.Join(m.config.ServiceWorkingCopy, "admin", "clients", record.ClientID+".json")
	auditPath := filepath.Join(m.config.ServiceWorkingCopy, "admin", "audit", record.OperationID+"-revoke.json")
	client := map[string]any{
		"schema": ClientSchema, "client_id": record.ClientID, "realm_id": record.RealmID,
		"public_key": record.InstallationPublicKey, "fingerprint": record.InstallationFingerprint,
		"state": "revoked", "created_at": record.CreatedAt, "activated_at": record.ActivatedAt,
		"revoked_at": record.RevokedAt, "revoke_reason": record.RevokeReason,
	}
	audit := map[string]any{
		"schema": AuditSchema, "event": "client_revoked", "operation_id": record.OperationID,
		"client_id": record.ClientID, "realm_id": record.RealmID, "reason": record.RevokeReason, "at": record.RevokedAt,
	}
	if err := atomicWriteJSON(clientPath, client, 0o600); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(auditPath), 0o700); err != nil {
		return 0, err
	}
	if err := atomicWriteJSON(auditPath, audit, 0o600); err != nil {
		return 0, err
	}
	if output, err := m.runner.Output(ctx, m.config.SVNBinary, "add", "--force", "--parents", "--non-interactive", "--no-auth-cache", clientPath, auditPath); err != nil {
		return 0, fmt.Errorf("svn add service revoke: %w: %s", err, sanitizeOutput(output))
	}
	message := "filees: revoke client " + record.ClientID
	if output, err := m.runner.Output(ctx, m.config.SVNBinary, "commit", "--non-interactive", "--no-auth-cache", "-m", message, m.config.ServiceWorkingCopy); err != nil {
		return 0, fmt.Errorf("svn commit service revoke: %w: %s", err, sanitizeOutput(output))
	}
	output, err := m.runner.Output(ctx, m.config.SVNBinary, "info", "--show-item", "last-changed-revision", "--non-interactive", "--no-auth-cache", clientPath)
	if err != nil {
		return 0, fmt.Errorf("svn info service revoke: %w: %s", err, sanitizeOutput(output))
	}
	revision, err := strconv.ParseInt(strings.TrimSpace(string(output)), 10, 64)
	if err != nil || revision <= 0 {
		return 0, errors.New("svn returned invalid revoke revision")
	}
	return revision, nil
}

func (m *Manager) publishServiceFiles(ctx context.Context, record Record, activatedAt time.Time) (int64, error) {
	formatPath := filepath.Join(m.config.ServiceWorkingCopy, "format.json")
	realmPath := filepath.Join(m.config.ServiceWorkingCopy, "admin", "realms", record.RealmID+".json")
	clientPath := filepath.Join(m.config.ServiceWorkingCopy, "admin", "clients", record.ClientID+".json")
	viewPath := filepath.Join(m.config.ServiceWorkingCopy, "clients", record.ClientID, "view.json")
	auditPath := filepath.Join(m.config.ServiceWorkingCopy, "admin", "audit", record.OperationID+".json")
	client := map[string]any{
		"schema": ClientSchema, "client_id": record.ClientID, "realm_id": record.RealmID,
		"public_key": record.InstallationPublicKey, "fingerprint": record.InstallationFingerprint,
		"state": "active", "created_at": record.CreatedAt, "activated_at": activatedAt,
	}
	repositories, err := m.mobileRepositoryEntries(record)
	if err != nil {
		return 0, err
	}
	view := map[string]any{
		"schema": ViewSchema, "client_id": record.ClientID, "realm_id": record.RealmID,
		"generation": 1, "generated_at": activatedAt, "client_role": "normal",
		"capabilities": map[string]any{"can_create_repositories": true},
		"repositories": repositories, "active_operations": []any{},
	}
	audit := map[string]any{
		"schema": AuditSchema, "event": "client_activated", "operation_id": record.OperationID,
		"client_id": record.ClientID, "realm_id": record.RealmID, "at": activatedAt,
	}
	format := map[string]any{"schema": FormatSchema, "format_version": 1}
	realm := Realm{Schema: RealmSchema, RealmID: record.RealmID, State: "active", CreatedAt: record.CreatedAt}
	if err := writeStableBootstrapJSON(formatPath, FormatSchema, format); err != nil {
		return 0, err
	}
	if err := ensureRealm(realmPath, realm); err != nil {
		return 0, err
	}
	for path, value := range map[string]any{clientPath: client, viewPath: view, auditPath: audit} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return 0, err
		}
		if err := atomicWriteJSON(path, value, 0o600); err != nil {
			return 0, err
		}
	}
	paths := []string{formatPath, realmPath, clientPath, viewPath, auditPath}
	args := append([]string{"add", "--force", "--parents", "--non-interactive", "--no-auth-cache"}, paths...)
	if output, err := m.runner.Output(ctx, m.config.SVNBinary, args...); err != nil {
		return 0, fmt.Errorf("svn add service activation: %w: %s", err, sanitizeOutput(output))
	}
	status, err := m.runner.Output(ctx, m.config.SVNBinary, append([]string{"status", "--quiet", "--non-interactive", "--no-auth-cache"}, paths...)...)
	if err != nil {
		return 0, fmt.Errorf("svn status service activation: %w: %s", err, sanitizeOutput(status))
	}
	if len(bytes.TrimSpace(status)) != 0 {
		message := "filees: activate client " + record.ClientID
		if output, err := m.runner.Output(ctx, m.config.SVNBinary, "commit", "--non-interactive", "--no-auth-cache", "-m", message, m.config.ServiceWorkingCopy); err != nil {
			return 0, fmt.Errorf("svn commit service activation: %w: %s", err, sanitizeOutput(output))
		}
	}
	output, err := m.runner.Output(ctx, m.config.SVNBinary, "info", "--show-item", "last-changed-revision", "--non-interactive", "--no-auth-cache", clientPath)
	if err != nil {
		return 0, fmt.Errorf("svn info service activation: %w: %s", err, sanitizeOutput(output))
	}
	revision, err := strconv.ParseInt(strings.TrimSpace(string(output)), 10, 64)
	if err != nil || revision <= 0 {
		return 0, errors.New("svn returned invalid service revision")
	}
	return revision, nil
}

func ensureRealm(path string, wanted Realm) error {
	var existing Realm
	err := readStrict(path, RealmSchema, &existing)
	if err == nil {
		if existing.RealmID != wanted.RealmID || existing.State != "active" || existing.CreatedAt.IsZero() {
			return fmt.Errorf("service realm file %s conflicts with approved realm", path)
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return atomicWriteJSON(path, wanted, 0o600)
}

func writeStableBootstrapJSON(path, schema string, value any) error {
	raw, err := os.ReadFile(path)
	if err == nil {
		var existing any
		if err := json.Unmarshal(raw, &existing); err != nil {
			return fmt.Errorf("service bootstrap file %s conflicts with schema %q", path, schema)
		}
		existingCanonical, _ := json.Marshal(existing)
		expectedCanonical, marshalErr := json.Marshal(value)
		if marshalErr != nil || !bytes.Equal(existingCanonical, expectedCanonical) {
			return fmt.Errorf("service bootstrap file %s conflicts with expected %s", path, schema)
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return atomicWriteJSON(path, value, 0o600)
}

func (m *Manager) renderAccessLocked() error {
	records, err := m.recordsLocked()
	if err != nil {
		return err
	}
	var keys, authz strings.Builder
	authz.WriteString("[/]\n* =\n\n[/proof]\n")
	for _, record := range records {
		if record.State != "staged" && record.State != "active" {
			continue
		}
		key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(record.InstallationPublicKey))
		if err != nil {
			return err
		}
		entryPath := m.config.ClientEntryPath
		if record.Kind == onboarding.KindMobile {
			if m.config.MobileEntryPath == "" {
				return fmt.Errorf("activation record %s is kind mobile but mobile_entry_path is not configured", record.ClientID)
			}
			entryPath = m.config.MobileEntryPath
		}
		fmt.Fprintf(&keys, "restrict,command=\"%s %s %s\" %s filees:%s\n", entryPath, record.OperationID, record.ClientID, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))), record.ClientID)
		fmt.Fprintf(&authz, "%s = r\n", record.ClientID)
	}
	for _, record := range records {
		if record.State == "active" {
			fmt.Fprintf(&authz, "\n[/clients/%s]\n%s = r\n", record.ClientID, record.ClientID)
		}
	}
	if err := atomicWrite(m.config.AuthorizedKeysFile, []byte(keys.String()), 0o600); err != nil {
		return err
	}
	return atomicWrite(m.config.AuthzFile, []byte(authz.String()), 0o600)
}

func (m *Manager) recordsLocked() ([]Record, error) {
	entries, err := os.ReadDir(m.recordsDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var records []Record
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		record, err := readRecord(filepath.Join(m.recordsDir(), entry.Name()))
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].ClientID < records[j].ClientID })
	return records, nil
}

func validateGrant(grant onboarding.ActivationGrant) (string, error) {
	for label, value := range map[string]string{"operation_id": grant.OperationID, "deploy_request_id": grant.DeployRequestID, "client_id": grant.ClientID, "realm_id": grant.RealmID} {
		if _, err := uuid.Parse(value); err != nil {
			return "", fmt.Errorf("activation %s must be a UUID", label)
		}
	}
	key, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(grant.InstallationPublicKey)))
	if err != nil || key.Type() != ssh.KeyAlgoED25519 || len(options) != 0 || len(bytes.TrimSpace(rest)) != 0 || comment == "" {
		return "", errors.New("activation key must be one commented Ed25519 public key")
	}
	if ssh.FingerprintSHA256(key) != grant.InstallationFingerprint {
		return "", errors.New("activation key fingerprint mismatch")
	}
	if grant.ExpiresAt.IsZero() {
		return "", errors.New("activation expiry is required")
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))) + " " + comment, nil
}

func sameGrant(record Record, grant onboarding.ActivationGrant, canonical string) bool {
	return record.OperationID == grant.OperationID && record.DeployRequestID == grant.DeployRequestID &&
		record.ClientID == grant.ClientID && record.RealmID == grant.RealmID && record.Kind == grant.Kind &&
		record.InstallationPublicKey == canonical && record.InstallationFingerprint == grant.InstallationFingerprint
}

func readRecord(path string) (Record, error) {
	var record Record
	if err := readStrict(path, RecordSchema, &record); err != nil {
		return Record{}, err
	}
	return record, nil
}

func readReceipt(path string) (Receipt, error) {
	var receipt Receipt
	if err := readStrict(path, ReceiptSchema, &receipt); err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}

func readStrict(path, schema string, value any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(raw) > 64*1024 {
		return errors.New("activation JSON exceeds 64 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("activation JSON contains trailing data")
	}
	var header struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(raw, &header); err != nil || header.Schema != schema {
		return fmt.Errorf("unexpected activation schema %q", header.Schema)
	}
	return nil
}

func atomicWriteJSON(path string, value any, mode os.FileMode) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(raw, '\n'), mode)
}

func atomicWrite(path string, raw []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".activation-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func sanitizeOutput(raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if len(text) > 512 {
		text = text[:512]
	}
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' || r >= 0x20 {
			return r
		}
		return -1
	}, text)
}

func (m *Manager) recordsDir() string           { return filepath.Join(m.config.Root, "records") }
func (m *Manager) proofsDir() string            { return filepath.Join(m.config.Root, "proofs") }
func (m *Manager) recordPath(id string) string  { return filepath.Join(m.recordsDir(), id+".json") }
func (m *Manager) receiptPath(id string) string { return filepath.Join(m.proofsDir(), id+".json") }
