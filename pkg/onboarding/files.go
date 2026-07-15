package onboarding

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	ticketsDir    = "tickets"
	operationsDir = "operations"
	auditDir      = "audit"
	lockName      = ".toolchain.lock"
	claimPrefix   = ".claim-"
	jsonSuffix    = ".json"
)

type Options struct {
	Clock            Clock
	Random           io.Reader
	PortAllocator    PortAllocator
	OTPPepper        []byte
	OperationTTL     time.Duration
	OTPAttempts      int
	ReversePortFirst uint16
	ReversePortLast  uint16
}

type Files struct {
	root             string
	clock            Clock
	random           io.Reader
	portAllocator    PortAllocator
	pepper           []byte
	operationTTL     time.Duration
	otpAttempts      int
	reversePortFirst uint16
	reversePortLast  uint16
}

// Open opens a directory protocol, not a database. Commands take a short
// advisory lock only for a filesystem transition and retain no background
// process or in-memory authority after they exit.
func Open(root string, opts Options) (*Files, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("onboarding root must be absolute")
	}
	if opts.Clock == nil {
		opts.Clock = ClockFunc(time.Now)
	}
	if opts.Random == nil {
		opts.Random = rand.Reader
	}
	if opts.PortAllocator == nil {
		opts.PortAllocator = PortAllocatorFunc(firstFreePort)
	}
	if len(opts.OTPPepper) < 32 {
		return nil, errors.New("OTP pepper must contain at least 32 bytes")
	}
	if opts.OperationTTL <= 0 {
		return nil, errors.New("operation TTL must be positive")
	}
	if opts.OTPAttempts <= 0 {
		return nil, errors.New("OTP attempts must be positive")
	}
	if opts.ReversePortFirst == 0 || opts.ReversePortLast < opts.ReversePortFirst {
		return nil, errors.New("invalid reverse port range")
	}
	root = filepath.Clean(root)
	for _, dir := range []string{root, filepath.Join(root, ticketsDir), filepath.Join(root, operationsDir), filepath.Join(root, auditDir)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create onboarding directory: %w", err)
		}
		if err := requirePrivateDirectory(dir); err != nil {
			return nil, err
		}
	}
	lockPath := filepath.Join(root, lockName)
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create toolchain lock: %w", err)
	}
	if err := lock.Close(); err != nil {
		return nil, err
	}
	if err := requirePrivateFile(lockPath); err != nil {
		return nil, err
	}
	s := &Files{root: root, clock: opts.Clock, random: opts.Random, portAllocator: opts.PortAllocator, pepper: append([]byte(nil), opts.OTPPepper...), operationTTL: opts.OperationTTL, otpAttempts: opts.OTPAttempts, reversePortFirst: opts.ReversePortFirst, reversePortLast: opts.ReversePortLast}
	if err := s.withLock(s.recoverClaimsLocked); err != nil {
		return nil, fmt.Errorf("recover onboarding claims: %w", err)
	}
	return s, nil
}

func (s *Files) Close() error { return nil }

func (s *Files) CreateTicket(email string, policy Policy, ttl time.Duration) (Ticket, error) {
	canonical, err := canonicalEmail(email)
	if err != nil {
		return Ticket{}, err
	}
	if err := validatePolicy(policy); err != nil {
		return Ticket{}, err
	}
	if ttl <= 0 {
		return Ticket{}, errors.New("ticket TTL must be positive")
	}
	id, err := randomUUID(s.random)
	if err != nil {
		return Ticket{}, fmt.Errorf("generate ticket ID: %w", err)
	}
	now := s.clock.Now().UTC()
	ticket := Ticket{Schema: TicketSchema, TicketID: id, EmailDeliveryAddress: canonical, ApprovedPolicy: policy, CreatedAt: now, ExpiresAt: now.Add(ttl)}
	err = s.withLock(func() error {
		path := s.ticketPath(canonical)
		if _, err := os.Lstat(path); err == nil {
			return ErrTicketExists
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := atomicWriteJSON(path, ticket); err != nil {
			return err
		}
		return s.writeAuditLocked(AuditEvent{Event: "ticket_created", Actor: "filees-admin", ObjectType: "ticket", ObjectID: ticket.TicketID, At: now})
	})
	return ticket, err
}

func (s *Files) RevokeTicket(ticketID string) error {
	if _, err := uuid.Parse(ticketID); err != nil {
		return errors.New("ticket_id must be a UUID")
	}
	return s.withLock(func() error {
		entries, err := s.readTicketsLocked()
		if err != nil {
			return err
		}
		for path, ticket := range entries {
			if ticket.TicketID != ticketID {
				continue
			}
			if err := os.Remove(path); err != nil {
				return err
			}
			if err := syncDirectory(filepath.Dir(path)); err != nil {
				return err
			}
			return s.writeAuditLocked(AuditEvent{Event: "ticket_revoked", Actor: "filees-admin", ObjectType: "ticket", ObjectID: ticketID, At: s.clock.Now().UTC()})
		}
		return ErrNotFound
	})
}

func (s *Files) ListTickets() ([]Ticket, error) {
	var result []Ticket
	err := s.withLock(func() error {
		entries, err := s.readTicketsLocked()
		if err != nil {
			return err
		}
		for _, ticket := range entries {
			result = append(result, ticket)
		}
		return nil
	})
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result, err
}

func (s *Files) Take(email, requestID string) (TakeReceipt, error) {
	canonical, err := canonicalEmail(email)
	if err != nil {
		return TakeReceipt{}, err
	}
	if _, err := uuid.Parse(requestID); err != nil {
		return TakeReceipt{}, errors.New("onboarding_request_id must be a UUID")
	}
	var receipt TakeReceipt
	err = s.withLock(func() error {
		if err := s.recoverClaimsLocked(); err != nil {
			return err
		}
		bundle, err := s.readBundlePathLocked(s.operationPath(requestID))
		if err == nil {
			receipt = receiptFor(bundle.Operation)
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		ticketPath := s.ticketPath(canonical)
		var ticket Ticket
		if err := readStrictJSON(ticketPath, TicketSchema, &ticket); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return ErrTicketUnavailable
			}
			return err
		}
		now := s.clock.Now().UTC()
		if !now.Before(ticket.ExpiresAt) {
			if err := os.Remove(ticketPath); err != nil {
				return err
			}
			if err := syncDirectory(filepath.Dir(ticketPath)); err != nil {
				return err
			}
			if err := s.writeAuditLocked(AuditEvent{Event: "ticket_expired", Actor: "filees-onboard", ObjectType: "ticket", ObjectID: ticket.TicketID, At: now}); err != nil {
				return err
			}
			return ErrTicketUnavailable
		}
		claimPath := s.claimPath(requestID)
		if _, err := os.Lstat(claimPath); err == nil {
			return ErrRequestConflict
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Rename(ticketPath, claimPath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return ErrTicketUnavailable
			}
			return err
		}
		if err := syncDirectory(filepath.Dir(ticketPath)); err != nil {
			return err
		}
		if err := syncDirectory(filepath.Dir(claimPath)); err != nil {
			return err
		}
		bundle, err = s.bundleFromTicketLocked(ticket, requestID, now)
		if err != nil {
			_ = os.Rename(claimPath, ticketPath)
			_ = syncDirectory(filepath.Dir(ticketPath))
			_ = syncDirectory(filepath.Dir(claimPath))
			return err
		}
		if err := atomicWriteJSON(claimPath, bundle); err != nil {
			return err
		}
		if err := os.Rename(claimPath, s.operationPath(requestID)); err != nil {
			return err
		}
		if err := syncDirectory(filepath.Join(s.root, operationsDir)); err != nil {
			return err
		}
		receipt = receiptFor(bundle.Operation)
		return nil
	})
	return receipt, err
}

func (s *Files) AuthenticateOTP(otp string) (AuthGrant, error) {
	normalized, locator, err := parseOTP(otp)
	if err != nil {
		return AuthGrant{}, ErrOTPInvalid
	}
	var grant AuthGrant
	err = s.withLock(func() error {
		path, bundle, err := s.findBundleLocked(func(bundle Bundle) bool { return bundle.Operation.OTPLocator == locator })
		if err != nil {
			return ErrOTPInvalid
		}
		op := &bundle.Operation
		now := s.clock.Now().UTC()
		if op.State != OperationAwaitingTunnel {
			return ErrOTPInvalid
		}
		if !now.Before(op.ExpiresAt) {
			op.State, op.OTPHash, op.OTPLocator = OperationExpired, "", ""
			bundle.addAudit("otp_expired", "filees-ssh-auth", now)
			if err := atomicWriteJSON(path, bundle); err != nil {
				return err
			}
			return ErrOTPExpired
		}
		if !hmac.Equal([]byte(op.OTPHash), []byte(s.hashOTP(normalized))) {
			op.AttemptsLeft--
			event := "otp_rejected"
			if op.AttemptsLeft <= 0 {
				op.AttemptsLeft, op.State, op.OTPHash, op.OTPLocator = 0, OperationOTPExhausted, "", ""
				event = "otp_exhausted"
			}
			bundle.addAudit(event, "filees-ssh-auth", now)
			if err := atomicWriteJSON(path, bundle); err != nil {
				return err
			}
			return ErrOTPInvalid
		}
		op.State, op.OTPHash, op.OTPLocator = OperationTunnelAuthorized, "", ""
		bundle.addAudit("tunnel_authorized", "filees-ssh-auth", now)
		if err := atomicWriteJSON(path, bundle); err != nil {
			return err
		}
		grant = AuthGrant{OperationID: op.OperationID, ApprovedPolicy: op.ApprovedPolicy, AssignedReversePort: op.AssignedReversePort, ExpiresAt: op.ExpiresAt}
		return nil
	})
	return grant, err
}

func (s *Files) MarkOutboxDelivered(messageID string) error {
	return s.withLock(func() error {
		path, bundle, err := s.findBundleLocked(func(bundle Bundle) bool { return bundle.Outbox.MessageID == messageID })
		if err != nil {
			return err
		}
		if bundle.Outbox.DeliveryState == DeliveryDelivered {
			return nil
		}
		now := s.clock.Now().UTC()
		bundle.Outbox.DeliveryState, bundle.Outbox.DeliveryAddress, bundle.Outbox.OTP, bundle.Outbox.DeliveredAt = DeliveryDelivered, "", "", &now
		bundle.addAudit("mail_delivered", "filees-mail", now)
		return atomicWriteJSON(path, bundle)
	})
}

func (s *Files) GetOperation(operationID string) (Operation, error) {
	var operation Operation
	err := s.withLock(func() error {
		_, bundle, err := s.findBundleLocked(func(bundle Bundle) bool { return bundle.Operation.OperationID == operationID })
		if err != nil {
			return err
		}
		operation = bundle.Operation
		return nil
	})
	return operation, err
}

func (s *Files) ListOutbox() ([]MailOutboxEntry, error) {
	var entries []MailOutboxEntry
	err := s.withLock(func() error {
		bundles, err := s.readBundlesLocked()
		if err != nil {
			return err
		}
		for _, bundle := range bundles {
			entries = append(entries, bundle.Outbox)
		}
		return nil
	})
	return entries, err
}

func (s *Files) ListAudit() ([]AuditEvent, error) {
	var events []AuditEvent
	err := s.withLock(func() error {
		bundles, err := s.readBundlesLocked()
		if err != nil {
			return err
		}
		for _, bundle := range bundles {
			events = append(events, bundle.Audit...)
		}
		paths, err := filepath.Glob(filepath.Join(s.root, auditDir, "*"+jsonSuffix))
		if err != nil {
			return err
		}
		for _, path := range paths {
			var event AuditEvent
			if err := readStrictJSON(path, AuditSchema, &event); err != nil {
				return err
			}
			events = append(events, event)
		}
		return nil
	})
	sort.Slice(events, func(i, j int) bool { return events[i].At.Before(events[j].At) })
	return events, err
}

func (s *Files) recoverClaimsLocked() error {
	for _, dir := range []string{ticketsDir, operationsDir, auditDir} {
		paths, err := filepath.Glob(filepath.Join(s.root, dir, ".write-*"))
		if err != nil {
			return err
		}
		for _, path := range paths {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if len(paths) > 0 {
			if err := syncDirectory(filepath.Join(s.root, dir)); err != nil {
				return err
			}
		}
	}
	paths, err := filepath.Glob(filepath.Join(s.root, operationsDir, claimPrefix+"*"+jsonSuffix))
	if err != nil {
		return err
	}
	for _, path := range paths {
		requestID := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), claimPrefix), jsonSuffix)
		if _, err := uuid.Parse(requestID); err != nil {
			return fmt.Errorf("invalid claim filename %q", filepath.Base(path))
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var header struct {
			Schema string `json:"schema"`
		}
		if err := json.Unmarshal(raw, &header); err != nil {
			return err
		}
		var bundle Bundle
		switch header.Schema {
		case TicketSchema:
			var ticket Ticket
			if err := decodeStrict(raw, TicketSchema, &ticket); err != nil {
				return err
			}
			bundle, err = s.bundleFromTicketLocked(ticket, requestID, s.clock.Now().UTC())
			if err != nil {
				return err
			}
			if err := atomicWriteJSON(path, bundle); err != nil {
				return err
			}
		case BundleSchema:
			if err := decodeStrict(raw, BundleSchema, &bundle); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported claim schema %q", header.Schema)
		}
		if err := os.Rename(path, s.operationPath(requestID)); err != nil {
			return err
		}
		if err := syncDirectory(filepath.Dir(path)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Files) bundleFromTicketLocked(ticket Ticket, requestID string, now time.Time) (Bundle, error) {
	operationID, err := randomUUID(s.random)
	if err != nil {
		return Bundle{}, err
	}
	messageID, err := randomUUID(s.random)
	if err != nil {
		return Bundle{}, err
	}
	otp, locator, err := generateOTP(s.random)
	if err != nil {
		return Bundle{}, err
	}
	port, err := s.allocatePortLocked(now)
	if err != nil {
		return Bundle{}, err
	}
	op := Operation{Schema: OperationSchema, OperationID: operationID, OnboardingRequestID: requestID, OTPHash: s.hashOTP(otp), OTPLocator: locator, ApprovedPolicy: ticket.ApprovedPolicy, AssignedReversePort: port, AttemptsLeft: s.otpAttempts, State: OperationAwaitingTunnel, CreatedAt: now, ExpiresAt: now.Add(s.operationTTL)}
	outbox := MailOutboxEntry{Schema: OutboxSchema, MessageID: messageID, OperationID: operationID, DeliveryAddress: ticket.EmailDeliveryAddress, OTP: otp, Template: "filees-onboarding-otp/v1", DeliveryState: DeliveryPending, CreatedAt: now}
	bundle := Bundle{Schema: BundleSchema, Operation: op, Outbox: outbox}
	bundle.addAudit("onboarding_started", "filees-onboard", now)
	return bundle, nil
}

func (s *Files) allocatePortLocked(now time.Time) (uint16, error) {
	used := make(map[uint16]bool)
	bundles, err := s.readBundlesLocked()
	if err != nil {
		return 0, err
	}
	for _, bundle := range bundles {
		op := bundle.Operation
		if now.Before(op.ExpiresAt) && (op.State == OperationAwaitingTunnel || op.State == OperationTunnelAuthorized) {
			used[op.AssignedReversePort] = true
		}
	}
	return s.portAllocator.Allocate(s.reversePortFirst, s.reversePortLast, func(port uint16) bool { return used[port] })
}

func (s *Files) readTicketsLocked() (map[string]Ticket, error) {
	paths, err := filepath.Glob(filepath.Join(s.root, ticketsDir, "*"+jsonSuffix))
	if err != nil {
		return nil, err
	}
	result := make(map[string]Ticket, len(paths))
	for _, path := range paths {
		var ticket Ticket
		if err := readStrictJSON(path, TicketSchema, &ticket); err != nil {
			return nil, err
		}
		result[path] = ticket
	}
	return result, nil
}

func (s *Files) readBundlesLocked() ([]Bundle, error) {
	paths, err := filepath.Glob(filepath.Join(s.root, operationsDir, "*"+jsonSuffix))
	if err != nil {
		return nil, err
	}
	var bundles []Bundle
	for _, path := range paths {
		if strings.HasPrefix(filepath.Base(path), claimPrefix) {
			continue
		}
		bundle, err := s.readBundlePathLocked(path)
		if err != nil {
			return nil, err
		}
		bundles = append(bundles, bundle)
	}
	return bundles, nil
}

func (s *Files) readBundlePathLocked(path string) (Bundle, error) {
	var bundle Bundle
	if err := readStrictJSON(path, BundleSchema, &bundle); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Bundle{}, ErrNotFound
		}
		return Bundle{}, err
	}
	return bundle, nil
}

func (s *Files) findBundleLocked(match func(Bundle) bool) (string, Bundle, error) {
	paths, err := filepath.Glob(filepath.Join(s.root, operationsDir, "*"+jsonSuffix))
	if err != nil {
		return "", Bundle{}, err
	}
	for _, path := range paths {
		if strings.HasPrefix(filepath.Base(path), claimPrefix) {
			continue
		}
		bundle, err := s.readBundlePathLocked(path)
		if err != nil {
			return "", Bundle{}, err
		}
		if match(bundle) {
			return path, bundle, nil
		}
	}
	return "", Bundle{}, ErrNotFound
}

func (s *Files) writeAuditLocked(event AuditEvent) error {
	event.Schema = AuditSchema
	name := fmt.Sprintf("%020d-%s-%s%s", event.At.UnixNano(), event.ObjectID, event.Event, jsonSuffix)
	return atomicWriteJSON(filepath.Join(s.root, auditDir, name), event)
}

func (bundle *Bundle) addAudit(event, actor string, at time.Time) {
	bundle.Audit = append(bundle.Audit, AuditEvent{Schema: AuditSchema, Event: event, Actor: actor, ObjectType: "operation", ObjectID: bundle.Operation.OperationID, OperationID: bundle.Operation.OperationID, At: at})
}

func (s *Files) ticketPath(email string) string {
	return filepath.Join(s.root, ticketsDir, hex.EncodeToString(emailKey(email))+jsonSuffix)
}
func (s *Files) claimPath(requestID string) string {
	return filepath.Join(s.root, operationsDir, claimPrefix+requestID+jsonSuffix)
}
func (s *Files) operationPath(requestID string) string {
	return filepath.Join(s.root, operationsDir, requestID+jsonSuffix)
}

func atomicWriteJSON(path string, value any) error {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".write-")
	if err != nil {
		return err
	}
	temp := file.Name()
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(temp)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temp, path); err != nil {
		return err
	}
	if err := syncDirectory(dir); err != nil {
		return err
	}
	ok = true
	return nil
}

func readStrictJSON(path, schema string, target any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("private record %s has unsafe type or permissions", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return decodeStrict(raw, schema, target)
}

func decodeStrict(raw []byte, schema string, target any) error {
	var header struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return fmt.Errorf("decode onboarding record: %w", err)
	}
	if header.Schema != schema {
		return fmt.Errorf("unsupported onboarding schema %q", header.Schema)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode onboarding record: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("decode onboarding record: trailing JSON value")
	}
	return nil
}

func requirePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("onboarding directory %s must be private", path)
	}
	return nil
}
func requirePrivateFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("onboarding file %s must be private", path)
	}
	return nil
}
func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func canonicalEmail(value string) (string, error) {
	value = strings.TrimSpace(value)
	if strings.Count(value, "@") != 1 {
		return "", errors.New("email must be a plain mailbox address")
	}
	parts := strings.SplitN(value, "@", 2)
	if !validLocalPart(parts[0]) || !validDomain(parts[1]) {
		return "", errors.New("email must be a plain mailbox address")
	}
	return parts[0] + "@" + strings.ToLower(parts[1]), nil
}
func validLocalPart(value string) bool {
	if value == "" || len(value) > 64 || value[0] == '.' || value[len(value)-1] == '.' || strings.Contains(value, "..") {
		return false
	}
	const punctuation = "!#$%&'*+-/=?^_`{|}~."
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && !strings.ContainsRune(punctuation, char) {
			return false
		}
	}
	return true
}
func validDomain(value string) bool {
	if value == "" || len(value) > 253 {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' {
				return false
			}
		}
	}
	return true
}
func validatePolicy(policy Policy) error {
	if _, err := uuid.Parse(policy.RealmID); err != nil {
		return errors.New("policy realm_id must be a UUID")
	}
	return nil
}
func emailKey(email string) []byte {
	digest := sha256.Sum256([]byte(email))
	return digest[:]
}
func randomUUID(reader io.Reader) (string, error) {
	id, err := uuid.NewRandomFromReader(reader)
	if err != nil {
		return "", err
	}
	return id.String(), nil
}
func generateOTP(reader io.Reader) (string, string, error) {
	raw := make([]byte, 15)
	if _, err := io.ReadFull(reader, raw); err != nil {
		return "", "", err
	}
	encoding := base32.StdEncoding.WithPadding(base32.NoPadding)
	locator := encoding.EncodeToString(raw[:5])
	secret := encoding.EncodeToString(raw[5:])
	return locator + "-" + secret, locator, nil
}
func parseOTP(value string) (string, string, error) {
	value = strings.ToUpper(strings.TrimSpace(value))
	parts := strings.Split(value, "-")
	if len(parts) != 2 || len(parts[0]) != 8 || len(parts[1]) != 16 {
		return "", "", ErrOTPInvalid
	}
	encoding := base32.StdEncoding.WithPadding(base32.NoPadding)
	if _, err := encoding.DecodeString(parts[0]); err != nil {
		return "", "", err
	}
	if _, err := encoding.DecodeString(parts[1]); err != nil {
		return "", "", err
	}
	return value, parts[0], nil
}
func (s *Files) hashOTP(otp string) string {
	mac := hmac.New(sha256.New, s.pepper)
	mac.Write([]byte(otp))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func receiptFor(op Operation) TakeReceipt {
	return TakeReceipt{OperationID: op.OperationID, OnboardingRequestID: op.OnboardingRequestID, ExpiresAt: op.ExpiresAt}
}

type Clock interface{ Now() time.Time }
type ClockFunc func() time.Time

func (fn ClockFunc) Now() time.Time { return fn() }

type PortAllocator interface {
	Allocate(first, last uint16, unavailable func(uint16) bool) (uint16, error)
}
type PortAllocatorFunc func(first, last uint16, unavailable func(uint16) bool) (uint16, error)

func (fn PortAllocatorFunc) Allocate(first, last uint16, unavailable func(uint16) bool) (uint16, error) {
	return fn(first, last, unavailable)
}
func firstFreePort(first, last uint16, unavailable func(uint16) bool) (uint16, error) {
	for port := uint32(first); port <= uint32(last); port++ {
		if !unavailable(uint16(port)) {
			return uint16(port), nil
		}
	}
	return 0, ErrNoReversePort
}

type MailSink interface {
	Deliver(context.Context, MailOutboxEntry) error
}
