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
	"filees/pkg/privatefile"
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
	// MobilePairingTTL bounds mobile pairing tokens (CreateMobilePairing) -
	// deliberately much shorter than OperationTTL, since a QR code is meant
	// to be scanned promptly, not redeemed hours later. Defaults to 5
	// minutes when zero, so existing callers/tests need no change.
	MobilePairingTTL time.Duration
}

const defaultMobilePairingTTL = 5 * time.Minute

// Area is a filesystem capability within the service repository. Callers
// declare the areas needed by one tool invocation before OpenExisting; the
// declaration is enforced on every platform and becomes the source for the
// tighter unveil profile on OpenBSD.
type Area uint8

const (
	AreaTickets Area = 1 << iota
	AreaOperations
	AreaAudit
	AreaAll = AreaTickets | AreaOperations | AreaAudit
)

type Access struct {
	Areas   Area
	NeedOTP bool
}

type Files struct {
	root             string
	areas            Area
	needOTP          bool
	clock            Clock
	random           io.Reader
	portAllocator    PortAllocator
	pepper           []byte
	operationTTL     time.Duration
	otpAttempts      int
	reversePortFirst uint16
	reversePortLast  uint16
	mobilePairingTTL time.Duration
}

// Open is the compatibility entry point used by embedded callers and tests.
// Server-side tools use Initialize during deployment and OpenExisting after
// installing their sandbox, so normal invocations never create or recover the
// repository outside unveil.
func Open(root string, opts Options) (*Files, error) {
	if err := Initialize(root); err != nil {
		return nil, err
	}
	s, err := OpenExisting(root, opts, Access{Areas: AreaAll, NeedOTP: true})
	if err != nil {
		return nil, err
	}
	if err := s.Recover(); err != nil {
		return nil, err
	}
	return s, nil
}

// Initialize creates the complete private service-repository layout. It is an
// explicit deployment operation, not part of opening the repository.
func Initialize(root string) error {
	if !filepath.IsAbs(root) {
		return errors.New("onboarding root must be absolute")
	}
	root = filepath.Clean(root)
	for _, dir := range []string{root, filepath.Join(root, ticketsDir), filepath.Join(root, operationsDir), filepath.Join(root, auditDir)} {
		// A directory that already exists is verified, never repaired. If it
		// was readable by others it may already have leaked, and silently
		// tightening it would hide that from the operator - Initialize is
		// reached from Open, so this is the check that refuses an unsafe root
		// rather than quietly adopting it.
		if _, err := os.Lstat(dir); err == nil {
			if err := requirePrivateDirectory(dir); err != nil {
				return err
			}
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		// Creating one is different: EnsureDir rather than MkdirAll, because on
		// Windows a directory made with mode 0700 still inherits its parent's
		// DACL - the mode claims private while the ACL grants other accounts
		// access. Same exposure r435 fixed for key material.
		if err := privatefile.EnsureDir(dir); err != nil {
			return fmt.Errorf("create onboarding directory: %w", err)
		}
		if err := requirePrivateDirectory(dir); err != nil {
			return err
		}
	}
	lockPath := filepath.Join(root, lockName)
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("create toolchain lock: %w", err)
	}
	if err := lock.Close(); err != nil {
		return err
	}
	return requirePrivateFile(lockPath)
}

// OpenExisting validates and opens only the declared repository areas. It
// does not create files and does not perform recovery.
func OpenExisting(root string, opts Options, access Access) (*Files, error) {
	if err := CheckExisting(root, access); err != nil {
		return nil, err
	}
	return OpenPrepared(root, opts, access)
}

// CheckExisting performs the read-only metadata preflight while the process
// still has its bootstrap rpath promise. Exact child unveils deliberately do
// not expose the service root itself for a later lstat.
func CheckExisting(root string, access Access) error {
	if !filepath.IsAbs(root) {
		return errors.New("onboarding root must be absolute")
	}
	if access.Areas == 0 || access.Areas&^AreaAll != 0 {
		return errors.New("onboarding access must declare valid areas")
	}
	root = filepath.Clean(root)
	if err := requirePrivateDirectory(root); err != nil {
		return err
	}
	for area, dir := range map[Area]string{
		AreaTickets:    filepath.Join(root, ticketsDir),
		AreaOperations: filepath.Join(root, operationsDir),
		AreaAudit:      filepath.Join(root, auditDir),
	} {
		if access.Areas&area == 0 {
			continue
		}
		if err := requirePrivateDirectory(dir); err != nil {
			return err
		}
	}
	return requirePrivateFile(filepath.Join(root, lockName))
}

// OpenPrepared constructs a handle after a successful CheckExisting and after
// the caller has installed unveil. It performs no filesystem access.
func OpenPrepared(root string, opts Options, access Access) (*Files, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("onboarding root must be absolute")
	}
	if access.Areas == 0 || access.Areas&^AreaAll != 0 {
		return nil, errors.New("onboarding access must declare valid areas")
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
	if access.NeedOTP && len(opts.OTPPepper) < 32 {
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
	if opts.MobilePairingTTL <= 0 {
		opts.MobilePairingTTL = defaultMobilePairingTTL
	}
	root = filepath.Clean(root)
	return &Files{root: root, areas: access.Areas, needOTP: access.NeedOTP, clock: opts.Clock, random: opts.Random, portAllocator: opts.PortAllocator, pepper: append([]byte(nil), opts.OTPPepper...), operationTTL: opts.OperationTTL, otpAttempts: opts.OTPAttempts, reversePortFirst: opts.ReversePortFirst, reversePortLast: opts.ReversePortLast, mobilePairingTTL: opts.MobilePairingTTL}, nil
}

func (s *Files) Close() error { return nil }

func (s *Files) requireAreas(areas Area) error {
	if s.areas&areas != areas {
		return fmt.Errorf("onboarding repository access does not include required areas %#x", areas)
	}
	return nil
}

func (s *Files) requireOTP() error {
	if !s.needOTP || len(s.pepper) < 32 {
		return errors.New("onboarding repository access does not include OTP secret")
	}
	return nil
}

// Recover completes interrupted atomic transitions. It is intentionally
// explicit so only filees-operation recover and compatibility Open invoke it.
func (s *Files) Recover() error {
	if err := s.requireAreas(AreaAll); err != nil {
		return err
	}
	if err := s.requireOTP(); err != nil {
		return err
	}
	if err := s.withLock(s.recoverClaimsLocked); err != nil {
		return fmt.Errorf("recover onboarding claims: %w", err)
	}
	if err := s.withLock(s.expireOperationsLocked); err != nil {
		return fmt.Errorf("expire onboarding operations: %w", err)
	}
	return nil
}

func (s *Files) expireOperationsLocked() error {
	paths, err := filepath.Glob(filepath.Join(s.root, operationsDir, "*"+jsonSuffix))
	if err != nil {
		return err
	}
	now := s.clock.Now().UTC()
	for _, path := range paths {
		if strings.HasPrefix(filepath.Base(path), claimPrefix) {
			continue
		}
		bundle, err := s.readBundlePathLocked(path)
		if err != nil {
			return err
		}
		op := &bundle.Operation
		if now.Before(op.ExpiresAt) || op.State == OperationActive || op.State == OperationExpired || op.State == OperationOTPExhausted {
			continue
		}
		op.State, op.OTPHash, op.OTPLocator, op.AttemptsLeft = OperationExpired, "", "", 0
		bundle.Outbox.OTP, bundle.Outbox.DeliveryAddress = "", ""
		bundle.addAudit("onboarding_expired", "filees-operation", now)
		if err := atomicWriteJSON(path, bundle); err != nil {
			return err
		}
	}
	return nil
}

func (s *Files) CreateTicket(email string, policy Policy, ttl time.Duration) (Ticket, error) {
	if err := s.requireAreas(AreaTickets | AreaAudit); err != nil {
		return Ticket{}, err
	}
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
		if taken, err := s.ticketAddressTakenLocked(path, now); err != nil {
			return err
		} else if taken {
			return ErrTicketExists
		}
		if err := atomicWriteJSON(path, ticket); err != nil {
			return err
		}
		return s.writeAuditLocked(AuditEvent{Event: "ticket_created", Actor: "filees-admin", ObjectType: "ticket", ObjectID: ticket.TicketID, At: now})
	})
	return ticket, err
}

// CreateInvitationTicket creates a desktop invitation. An empty realmID
// leaves the realm unbound - AuthenticateOTP mints a fresh one when the
// invitation is redeemed, the ordinary new-realm path. A non-empty realmID
// (already resolved and validated by the caller - see
// internal/servertool/admin.go's --join-realm-alias and
// pkg/repoworker's RealmJoinRequestStore, the only two legitimate sources)
// is baked into the ticket and is the *only* thing that can ever bind a
// new installation to an existing realm; the redeeming client's own
// proposal is never authoritative (concepts/REALM_JOIN_AUTHORIZATION_CONCEPT.md).
// The raw capability is stored only in the pending mail entry; after
// successful relay acceptance it is scrubbed from server state and remains
// only in the recipient's invitation message.
func (s *Files) CreateInvitationTicket(email string, ttl time.Duration, profile Invitation, realmID string) (Ticket, error) {
	if err := s.requireAreas(AreaTickets | AreaAudit); err != nil {
		return Ticket{}, err
	}
	canonical, err := canonicalEmail(email)
	if err != nil {
		return Ticket{}, err
	}
	if ttl <= 0 {
		return Ticket{}, errors.New("ticket TTL must be positive")
	}
	if realmID != "" {
		if _, err := uuid.Parse(realmID); err != nil {
			return Ticket{}, errors.New("realm ID must be a UUID")
		}
	}
	token, err := newInvitationToken(s.random)
	if err != nil {
		return Ticket{}, err
	}
	profile.Schema, profile.Token = InvitationSchema, token
	wire, err := EncodeInvitation(profile)
	if err != nil {
		return Ticket{}, err
	}
	id, err := randomUUID(s.random)
	if err != nil {
		return Ticket{}, fmt.Errorf("generate ticket ID: %w", err)
	}
	messageID, err := randomUUID(s.random)
	if err != nil {
		return Ticket{}, fmt.Errorf("generate invitation message ID: %w", err)
	}
	now := s.clock.Now().UTC()
	ticket := Ticket{
		Schema: TicketSchema, TicketID: id, EmailDeliveryAddress: canonical,
		ApprovedPolicy: Policy{Kind: KindDesktop, RealmID: realmID}, InvitationTokenHash: invitationTokenHash(token),
		InvitationOutbox: MailOutboxEntry{Schema: OutboxSchema, MessageID: messageID, DeliveryAddress: canonical, Invitation: wire, Template: InvitationMailTemplate, DeliveryState: DeliveryPending, CreatedAt: now},
		CreatedAt:        now, ExpiresAt: now.Add(ttl),
	}
	err = s.withLock(func() error {
		path := s.ticketPath(canonical)
		if taken, err := s.ticketAddressTakenLocked(path, now); err != nil {
			return err
		} else if taken {
			return ErrTicketExists
		}
		if err := atomicWriteJSON(path, ticket); err != nil {
			return err
		}
		return s.writeAuditLocked(AuditEvent{Event: "ticket_invited", Actor: "filees-admin", ObjectType: "ticket", ObjectID: ticket.TicketID, At: now})
	})
	return ticket, err
}

// ResendInvitation replaces a failed or expired-delivery invitation capability
// without changing the ticket's recipient, policy or TTL.
func (s *Files) ResendInvitation(ticketID string, profile Invitation) error {
	if err := s.requireAreas(AreaTickets | AreaAudit); err != nil {
		return err
	}
	if _, err := uuid.Parse(ticketID); err != nil {
		return errors.New("ticket_id must be a UUID")
	}
	return s.withLock(func() error {
		entries, err := s.readTicketsLocked()
		if err != nil {
			return err
		}
		var path string
		var ticket Ticket
		for candidatePath, candidate := range entries {
			if candidate.TicketID == ticketID {
				path, ticket = candidatePath, candidate
				break
			}
		}
		if path == "" {
			return ErrNotFound
		}
		now := s.clock.Now().UTC()
		if !now.Before(ticket.ExpiresAt) {
			return ErrTicketUnavailable
		}
		if ticket.InvitationOutbox.DeliveryState == DeliveryPending || ticket.InvitationOutbox.DeliveryState == DeliverySending {
			return errors.New("invitation delivery is already pending")
		}
		token, err := newInvitationToken(s.random)
		if err != nil {
			return err
		}
		profile.Schema, profile.Token = InvitationSchema, token
		wire, err := EncodeInvitation(profile)
		if err != nil {
			return err
		}
		messageID, err := randomUUID(s.random)
		if err != nil {
			return err
		}
		ticket.InvitationTokenHash = invitationTokenHash(token)
		ticket.InvitationOutbox = MailOutboxEntry{Schema: OutboxSchema, MessageID: messageID, DeliveryAddress: ticket.EmailDeliveryAddress, Invitation: wire, Template: InvitationMailTemplate, DeliveryState: DeliveryPending, CreatedAt: now}
		if err := atomicWriteJSON(path, ticket); err != nil {
			return err
		}
		return s.writeAuditLocked(AuditEvent{Event: "ticket_invitation_resent", Actor: "filees-admin", ObjectType: "ticket", ObjectID: ticket.TicketID, At: now})
	})
}

// CreateMobilePairing mints a mobile pairing operation directly - no
// Ticket, no e-mail, no MailOutboxEntry. Mobile has no admin-ticket
// self-activation path at all (architectural decision: mobile can only be
// paired by an already-active desktop of the same realm, via its own
// authenticated control-plane request - see concepts/
// FILEES_ANDROID_CLIENT_CONCEPT_V2.md). The desktop displays the returned
// token as a QR code; the phone presents it exactly where a desktop client
// would present its mailed OTP (the same _filees-tunnel BSD-Auth class,
// AuthenticateOTP, completely unchanged). realmID is supplied by the
// caller's already-authenticated session (e.g. repoworker.Session.RealmID,
// resolved server-side from the connection, never from a client payload) -
// this method does not and must not accept a client_id or installation key.
func (s *Files) CreateMobilePairing(realmID string, repos []MobileRepositoryGrant) (string, TakeReceipt, error) {
	if err := s.requireAreas(AreaOperations | AreaAudit); err != nil {
		return "", TakeReceipt{}, err
	}
	if err := s.requireOTP(); err != nil {
		return "", TakeReceipt{}, err
	}
	if _, err := uuid.Parse(realmID); err != nil {
		return "", TakeReceipt{}, errors.New("realm_id must be a UUID")
	}
	requestID, err := randomUUID(s.random)
	if err != nil {
		return "", TakeReceipt{}, err
	}
	operationID, err := randomUUID(s.random)
	if err != nil {
		return "", TakeReceipt{}, err
	}
	clientID, err := randomUUID(s.random)
	if err != nil {
		return "", TakeReceipt{}, err
	}
	token, locator, err := generateOTP(s.random)
	if err != nil {
		return "", TakeReceipt{}, err
	}
	var receipt TakeReceipt
	err = s.withLock(func() error {
		now := s.clock.Now().UTC()
		op := Operation{
			Schema: OperationSchema, OperationID: operationID, ClientID: clientID, OnboardingRequestID: requestID,
			OTPHash: s.hashOTP(token), OTPLocator: locator, ApprovedPolicy: Policy{RealmID: realmID, Kind: KindMobile, Repositories: repos},
			AttemptsLeft: s.otpAttempts, State: OperationAwaitingTunnel, CreatedAt: now, ExpiresAt: now.Add(s.mobilePairingTTL),
		}
		bundle := Bundle{Schema: BundleSchema, Operation: op}
		bundle.addAudit("mobile_pairing_created", "filees-mobile-pairing", now)
		if err := atomicWriteJSON(s.operationPath(requestID), bundle); err != nil {
			return err
		}
		if err := syncDirectory(filepath.Join(s.root, operationsDir)); err != nil {
			return err
		}
		receipt = receiptFor(op)
		return nil
	})
	return token, receipt, err
}

func (s *Files) RevokeTicket(ticketID string) error {
	if err := s.requireAreas(AreaTickets | AreaAudit); err != nil {
		return err
	}
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
	if err := s.requireAreas(AreaTickets); err != nil {
		return nil, err
	}
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
	if err := s.requireAreas(AreaAll); err != nil {
		return TakeReceipt{}, err
	}
	if err := s.requireOTP(); err != nil {
		return TakeReceipt{}, err
	}
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

// TakeInvitation consumes an administrator-issued opaque invitation. The
// caller proposes a realm UUID, but it is held only on the pending operation;
// AuthenticateOTP makes it the approved realm after the e-mail recipient has
// proven possession of the second factor.
func (s *Files) TakeInvitation(token, proposedRealmID, requestID string) (TakeReceipt, error) {
	if err := s.requireAreas(AreaAll); err != nil {
		return TakeReceipt{}, err
	}
	if err := s.requireOTP(); err != nil {
		return TakeReceipt{}, err
	}
	if !validInvitationToken(token) || !validRealmID(proposedRealmID) {
		return TakeReceipt{}, ErrTicketUnavailable
	}
	if _, err := uuid.Parse(requestID); err != nil {
		return TakeReceipt{}, errors.New("onboarding_request_id must be a UUID")
	}
	wantedHash := invitationTokenHash(token)
	var receipt TakeReceipt
	err := s.withLock(func() error {
		if err := s.recoverClaimsLocked(); err != nil {
			return err
		}
		bundle, err := s.readBundlePathLocked(s.operationPath(requestID))
		if err == nil {
			if bundle.Operation.ProposedRealmID != proposedRealmID {
				return ErrRequestConflict
			}
			receipt = receiptFor(bundle.Operation)
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		entries, err := s.readTicketsLocked()
		if err != nil {
			return err
		}
		var ticketPath string
		var ticket Ticket
		for path, candidate := range entries {
			if candidate.InvitationTokenHash == "" {
				continue
			}
			if hmac.Equal([]byte(candidate.InvitationTokenHash), []byte(wantedHash)) {
				ticketPath, ticket = path, candidate
				break
			}
		}
		if ticketPath == "" {
			return ErrTicketUnavailable
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
		bundle.Operation.ProposedRealmID = proposedRealmID
		bundle.addAudit("invitation_accepted", "filees-onboard", now)
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
	if err := s.requireAreas(AreaOperations); err != nil {
		return AuthGrant{}, err
	}
	if err := s.requireOTP(); err != nil {
		return AuthGrant{}, err
	}
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
		// The realm is decided here, never by the client: op.ApprovedPolicy.RealmID
		// is either already set from the ticket (filees-admin ticket create
		// --join-realm-alias, or a confirmed self-service join-request -
		// pkg/repoworker's RealmJoinRequestStore) or gets freshly generated
		// right here for an ordinary new-realm ticket. op.ProposedRealmID -
		// the client's own locally-generated proposal - is read from the
		// wire for backward compatibility with already-deployed clients but
		// is never consulted for this decision; the OTP only proves
		// possession of the invited mailbox, never authorization to name or
		// join a realm. See concepts/REALM_JOIN_AUTHORIZATION_CONCEPT.md.
		if op.ApprovedPolicy.RealmID == "" {
			realmID, err := randomUUID(s.random)
			if err != nil {
				return err
			}
			op.ApprovedPolicy.RealmID = realmID
			bundle.addAudit("realm_created", "filees-ssh-auth", now)
		} else {
			bundle.addAudit("realm_bound", "filees-ssh-auth", now)
		}
		op.ProposedRealmID = ""
		if !validRealmID(op.ApprovedPolicy.RealmID) {
			return ErrOTPInvalid
		}
		// OTPLocator is not secret (only OTPHash guards the actual secret
		// half) and is kept live past this transition: mobile onboarding's
		// ClaimAuthorizedMobilePush needs it as the rendezvous key between
		// this BSD-Auth session and the separately exec'd forced command,
		// since OpenBSD sshd does not propagate BSD-Auth state into the
		// command environment (see ClaimAuthorizedTunnel's doc comment
		// below). It is blanked once genuinely consumed there, or at
		// terminal expiry/activation as before.
		op.State, op.OTPHash = OperationTunnelAuthorized, ""
		bundle.addAudit("tunnel_authorized", "filees-ssh-auth", now)
		if err := atomicWriteJSON(path, bundle); err != nil {
			return err
		}
		grant = authGrantFor(*op)
		return nil
	})
	return grant, err
}

// ClaimAuthorizedTunnel is the filesystem handoff from BSD Authentication to
// the forced command. OpenBSD sshd does not propagate BSD Auth setenv values
// into the command environment, so the operation bundle is the explicit
// one-time rendezvous. S2 uses one fixed PermitListen port; more than one
// matching grant is therefore always a hard error.
func (s *Files) ClaimAuthorizedTunnel(expectedPort uint16) (AuthGrant, error) {
	if err := s.requireAreas(AreaOperations); err != nil {
		return AuthGrant{}, err
	}
	if expectedPort == 0 {
		return AuthGrant{}, ErrTunnelGrant
	}
	var grant AuthGrant
	err := s.withLock(func() error {
		paths, err := filepath.Glob(filepath.Join(s.root, operationsDir, "*"+jsonSuffix))
		if err != nil {
			return err
		}
		var selectedPath string
		var selected Bundle
		now := s.clock.Now().UTC()
		for _, path := range paths {
			if strings.HasPrefix(filepath.Base(path), claimPrefix) {
				continue
			}
			bundle, err := s.readBundlePathLocked(path)
			if err != nil {
				return err
			}
			op := bundle.Operation
			if op.State != OperationTunnelAuthorized || op.AssignedReversePort != expectedPort || !now.Before(op.ExpiresAt) {
				continue
			}
			if selectedPath != "" {
				return ErrTunnelGrant
			}
			selectedPath, selected = path, bundle
		}
		if selectedPath == "" {
			return ErrTunnelGrant
		}
		selected.Operation.State = OperationTunnelStarted
		selected.addAudit("tunnel_session_started", "filees-entry", now)
		if err := atomicWriteJSON(selectedPath, selected); err != nil {
			return err
		}
		op := selected.Operation
		grant = authGrantFor(op)
		return nil
	})
	return grant, err
}

// ClaimAuthorizedMobilePush is the mobile-onboarding rendezvous: unlike
// ClaimAuthorizedTunnel/ClaimAuthorizedHelper, there is no reverse-forwarded
// port to correlate on, because mobile never sets one up (KindMobile skips
// the reverse-tunnel dance entirely - a public key is not secret and can be
// pushed directly over the same OTP-authenticated session that just
// authenticated it). The BSD-Auth rendezvous problem cannot be solved the
// way ClaimAuthorizedTunnel solves it (a scarce, per-operation resource like
// AssignedReversePort that structurally admits only one live operation at a
// time) because mobile allocates no such resource - so the caller resends
// the pairing token itself alongside the pushed key (it already possesses
// it, having scanned it from the QR, and the payload only ever travels over
// the session that same token just authenticated - not a fresh exposure).
// The token's locator half re-derives the exact operation instead of
// scanning for "the only one", so two concurrent mobile pairings - or a
// mobile pairing racing a concurrent desktop deploy also sitting in
// OperationTunnelAuthorized - can never claim each other's operation.
// ApprovedPolicy.Kind is checked too, as defense in depth. This claims the
// exact matched operation, records the pushed installation public key, and
// transitions straight to OperationIdentityGenerated - skipping
// OperationTunnelStarted/OperationHelperAnnounced, which only exist to
// track reverse-tunnel handshake sub-steps mobile never performs.
func (s *Files) ClaimAuthorizedMobilePush(token, publicKey, fingerprint string) (ActivationGrant, error) {
	if err := s.requireAreas(AreaOperations); err != nil {
		return ActivationGrant{}, err
	}
	_, locator, err := parseOTP(token)
	if err != nil {
		return ActivationGrant{}, ErrOTPInvalid
	}
	publicKey, fingerprint = strings.TrimSpace(publicKey), strings.TrimSpace(fingerprint)
	if fingerprint == "" {
		return ActivationGrant{}, ErrTunnelGrant
	}
	// The pushed key must be bare (no comment): the client generates its
	// identity fully offline, before it has ever talked to the server, so it
	// cannot know its own client_id yet to embed in a "filees:<clientID>"
	// comment the way activation.validateGrant requires. The server already
	// minted client_id in CreateMobilePairing, before the token was ever
	// shown - exactly like desktop's identity generator receives its
	// client_id from the server's HelperRequest before commenting its own
	// key (pkg/deploy/identity.go). So the comment is appended here, once
	// the operation (and its client_id) is found below, not supplied by the
	// client.
	if err := validateBareEd25519PublicKey(publicKey); err != nil {
		return ActivationGrant{}, err
	}
	var grant ActivationGrant
	err = s.withLock(func() error {
		paths, err := filepath.Glob(filepath.Join(s.root, operationsDir, "*"+jsonSuffix))
		if err != nil {
			return err
		}
		var selectedPath string
		var selected Bundle
		now := s.clock.Now().UTC()
		for _, path := range paths {
			if strings.HasPrefix(filepath.Base(path), claimPrefix) {
				continue
			}
			bundle, err := s.readBundlePathLocked(path)
			if err != nil {
				return err
			}
			op := bundle.Operation
			if op.State != OperationTunnelAuthorized || !now.Before(op.ExpiresAt) ||
				op.OTPLocator != locator || op.ApprovedPolicy.Kind != KindMobile {
				continue
			}
			if selectedPath != "" {
				return ErrTunnelGrant
			}
			selectedPath, selected = path, bundle
		}
		if selectedPath == "" {
			return ErrTunnelGrant
		}
		if selected.Operation.DeployRequestID == "" {
			// Mobile has no reverse-tunnel deploy session to correlate (that
			// concept is desktop-specific, see ClaimAuthorizedHelper), but
			// activation.validateGrant still requires DeployRequestID to be a
			// UUID - synthesize one, exactly as ClientID already is when a
			// fresh operation has none.
			deployRequestID, err := randomUUID(s.random)
			if err != nil {
				return err
			}
			selected.Operation.DeployRequestID = deployRequestID
		}
		selected.Operation.State = OperationIdentityGenerated
		selected.Operation.OTPLocator = ""
		selected.Operation.InstallationPublicKey = publicKey + " filees:" + selected.Operation.ClientID
		selected.Operation.InstallationFingerprint = fingerprint
		selected.addAudit("mobile_key_pushed", "filees-mobile-onboard", now)
		selected.addAudit("identity_generated", "filees-mobile-onboard", now)
		if err := atomicWriteJSON(selectedPath, selected); err != nil {
			return err
		}
		grant = activationGrantFor(selected.Operation)
		return nil
	})
	return grant, err
}

// ClaimAuthorizedHelper atomically binds the one active OTP grant to the
// helper announced by the authenticated outer SSH session. The helper key is
// public material; the worker private key remains server-only.
func (s *Files) ClaimAuthorizedHelper(expectedPort uint16, deployRequestID, helperHostPublicKey, reconnectPublicKey string) (AuthGrant, error) {
	if err := s.requireAreas(AreaOperations); err != nil {
		return AuthGrant{}, err
	}
	if err := validateBareEd25519PublicKey(reconnectPublicKey); err != nil {
		return AuthGrant{}, err
	}
	if expectedPort == 0 || strings.TrimSpace(helperHostPublicKey) == "" || strings.TrimSpace(reconnectPublicKey) == "" {
		return AuthGrant{}, ErrTunnelGrant
	}
	if _, err := uuid.Parse(deployRequestID); err != nil {
		return AuthGrant{}, errors.New("deploy_request_id must be a UUID")
	}
	var grant AuthGrant
	err := s.withLock(func() error {
		paths, err := filepath.Glob(filepath.Join(s.root, operationsDir, "*"+jsonSuffix))
		if err != nil {
			return err
		}
		var selectedPath string
		var selected Bundle
		resume := false
		now := s.clock.Now().UTC()
		for _, path := range paths {
			if strings.HasPrefix(filepath.Base(path), claimPrefix) {
				continue
			}
			bundle, err := s.readBundlePathLocked(path)
			if err != nil {
				return err
			}
			op := bundle.Operation
			if op.AssignedReversePort != expectedPort {
				continue
			}
			candidateResume := helperDeploymentInProgress(op.State) && op.DeployRequestID == deployRequestID && op.HelperHostPublicKey == strings.TrimSpace(helperHostPublicKey) && op.ReconnectPublicKey == strings.TrimSpace(reconnectPublicKey)
			candidateFresh := op.State == OperationTunnelAuthorized && now.Before(op.ExpiresAt)
			if !candidateResume && !candidateFresh {
				continue
			}
			if candidateResume && op.State != OperationActive && !now.Before(op.ExpiresAt) {
				continue
			}
			if selectedPath != "" {
				return ErrTunnelGrant
			}
			selectedPath, selected = path, bundle
			resume = candidateResume
		}
		if selectedPath == "" {
			return ErrTunnelGrant
		}
		if resume {
			grant = authGrantFor(selected.Operation)
			return nil
		}
		selected.Operation.State = OperationHelperAnnounced
		if selected.Operation.ClientID == "" {
			selected.Operation.ClientID, err = randomUUID(s.random)
			if err != nil {
				return err
			}
		}
		selected.Operation.DeployRequestID = deployRequestID
		selected.Operation.HelperHostPublicKey = strings.TrimSpace(helperHostPublicKey)
		selected.Operation.ReconnectPublicKey = strings.TrimSpace(reconnectPublicKey)
		selected.addAudit("tunnel_session_started", "filees-worker", now)
		selected.addAudit("helper_announced", "filees-worker", now)
		if err := atomicWriteJSON(selectedPath, selected); err != nil {
			return err
		}
		grant = authGrantFor(selected.Operation)
		return nil
	})
	return grant, err
}

func helperDeploymentInProgress(state OperationState) bool {
	switch state {
	case OperationHelperAnnounced, OperationIdentityGenerated, OperationAccessStaged, OperationPossessionProved, OperationActive:
		return true
	default:
		return false
	}
}

// CompleteGeneratedIdentity publishes only the installation public key and
// fingerprint. Client-private paths and key material never enter the bundle.
func (s *Files) CompleteGeneratedIdentity(operationID, deployRequestID, publicKey, fingerprint string) error {
	if err := s.requireAreas(AreaOperations); err != nil {
		return err
	}
	return s.withLock(func() error {
		path, bundle, err := s.findBundleLocked(func(bundle Bundle) bool { return bundle.Operation.OperationID == operationID })
		if err != nil {
			return err
		}
		op := &bundle.Operation
		if op.State == OperationIdentityGenerated && op.DeployRequestID == deployRequestID && op.InstallationPublicKey == publicKey && op.InstallationFingerprint == fingerprint {
			return nil
		}
		if op.State != OperationHelperAnnounced || op.DeployRequestID != deployRequestID || strings.TrimSpace(publicKey) == "" || strings.TrimSpace(fingerprint) == "" {
			return ErrTunnelGrant
		}
		op.State = OperationIdentityGenerated
		op.InstallationPublicKey = strings.TrimSpace(publicKey)
		op.InstallationFingerprint = strings.TrimSpace(fingerprint)
		bundle.addAudit("helper_verified", "filees-worker", s.clock.Now().UTC())
		bundle.addAudit("identity_generated", "filees-worker", s.clock.Now().UTC())
		return atomicWriteJSON(path, bundle)
	})
}

// PendingActivation returns the unique live operation at the requested
// activation boundary. The fixed reverse-forward policy serializes deploys;
// ambiguity is a hard failure rather than an arbitrary selection.
func (s *Files) PendingActivation(state OperationState) (ActivationGrant, error) {
	if err := s.requireAreas(AreaOperations); err != nil {
		return ActivationGrant{}, err
	}
	var grant ActivationGrant
	err := s.withLock(func() error {
		bundles, err := s.readBundlesLocked()
		if err != nil {
			return err
		}
		now := s.clock.Now().UTC()
		for _, bundle := range bundles {
			op := bundle.Operation
			if op.State != state || !now.Before(op.ExpiresAt) {
				continue
			}
			if grant.OperationID != "" {
				return ErrTunnelGrant
			}
			grant = activationGrantFor(op)
		}
		if grant.OperationID == "" {
			return ErrTunnelGrant
		}
		return nil
	})
	return grant, err
}

func (s *Files) CompleteAccessStaged(operationID, deployRequestID string) error {
	return s.advanceActivation(operationID, deployRequestID, OperationIdentityGenerated, OperationAccessStaged, "access_staged")
}

func (s *Files) CompletePossessionProof(operationID, deployRequestID string) error {
	return s.advanceActivation(operationID, deployRequestID, OperationAccessStaged, OperationPossessionProved, "possession_verified")
}

func (s *Files) CompleteActivation(operationID, deployRequestID string, serviceRevision int64) error {
	if serviceRevision <= 0 {
		return errors.New("service revision must be positive")
	}
	if err := s.requireAreas(AreaOperations); err != nil {
		return err
	}
	return s.withLock(func() error {
		path, bundle, err := s.findBundleLocked(func(bundle Bundle) bool { return bundle.Operation.OperationID == operationID })
		if err != nil {
			return err
		}
		op := &bundle.Operation
		if op.State == OperationActive && op.DeployRequestID == deployRequestID && op.ServiceRevision == serviceRevision {
			return nil
		}
		if op.State != OperationPossessionProved || op.DeployRequestID != deployRequestID {
			return ErrTunnelGrant
		}
		now := s.clock.Now().UTC()
		op.State, op.ServiceRevision, op.ActivatedAt = OperationActive, serviceRevision, &now
		op.OTPHash, op.OTPLocator, op.AttemptsLeft = "", "", 0
		bundle.addAudit("client_activated", "filees-activate", now)
		return atomicWriteJSON(path, bundle)
	})
}

func (s *Files) advanceActivation(operationID, deployRequestID string, from, to OperationState, event string) error {
	if err := s.requireAreas(AreaOperations); err != nil {
		return err
	}
	return s.withLock(func() error {
		path, bundle, err := s.findBundleLocked(func(bundle Bundle) bool { return bundle.Operation.OperationID == operationID })
		if err != nil {
			return err
		}
		op := &bundle.Operation
		if op.State == to && op.DeployRequestID == deployRequestID {
			return nil
		}
		if op.State != from || op.DeployRequestID != deployRequestID {
			return ErrTunnelGrant
		}
		op.State = to
		bundle.addAudit(event, "filees-activate", s.clock.Now().UTC())
		return atomicWriteJSON(path, bundle)
	})
}

func authGrantFor(op Operation) AuthGrant {
	return AuthGrant{OperationID: op.OperationID, ClientID: op.ClientID, ApprovedPolicy: op.ApprovedPolicy, AssignedReversePort: op.AssignedReversePort, ExpiresAt: op.ExpiresAt}
}

func activationGrantFor(op Operation) ActivationGrant {
	return ActivationGrant{
		OperationID: op.OperationID, ClientID: op.ClientID, RealmID: op.ApprovedPolicy.RealmID,
		Kind: op.ApprovedPolicy.Kind, Repositories: op.ApprovedPolicy.Repositories, DeployRequestID: op.DeployRequestID, InstallationPublicKey: op.InstallationPublicKey,
		InstallationFingerprint: op.InstallationFingerprint, ExpiresAt: op.ExpiresAt,
	}
}

func (s *Files) ClaimPendingMail(staleAfter time.Duration) (MailJob, error) {
	if err := s.requireAreas(AreaTickets | AreaOperations | AreaAudit); err != nil {
		return MailJob{}, err
	}
	if staleAfter <= 0 {
		return MailJob{}, errors.New("mail claim stale timeout must be positive")
	}
	var job MailJob
	err := s.withLock(func() error {
		// Invitation messages live with their still-unconsumed ticket. They
		// are deliberately claimed before OTP mail: an invitation never
		// allocates a reverse port or creates an onboarding operation.
		ticketPaths, err := filepath.Glob(filepath.Join(s.root, ticketsDir, "*"+jsonSuffix))
		if err != nil {
			return err
		}
		now := s.clock.Now().UTC()
		for _, path := range ticketPaths {
			ticket, err := s.readTicketPathLocked(path)
			if err != nil {
				return err
			}
			entry := &ticket.InvitationOutbox
			if entry.MessageID == "" {
				continue
			}
			if entry.DeliveryState == DeliverySending && entry.AttemptedAt != nil && now.Sub(*entry.AttemptedAt) >= staleAfter {
				entry.DeliveryState, entry.AttemptID, entry.AttemptedAt = DeliveryPending, "", nil
				entry.LastError = "previous sender exited before recording a result"
			}
			if entry.DeliveryState != DeliveryPending {
				continue
			}
			if !now.Before(ticket.ExpiresAt) {
				entry.DeliveryState, entry.DeliveryAddress, entry.Invitation = DeliveryFailed, "", ""
				entry.LastError = "ticket expired before invitation submission"
				if err := atomicWriteJSON(path, ticket); err != nil {
					return err
				}
				continue
			}
			attemptID, err := randomUUID(s.random)
			if err != nil {
				return err
			}
			entry.DeliveryState, entry.AttemptID, entry.AttemptedAt, entry.LastError = DeliverySending, attemptID, &now, ""
			entry.RetryCount++
			if err := atomicWriteJSON(path, ticket); err != nil {
				return err
			}
			if err := s.writeAuditLocked(AuditEvent{Event: "invitation_mail_attempt_started", Actor: "filees-mail", ObjectType: "ticket", ObjectID: ticket.TicketID, At: now}); err != nil {
				return err
			}
			job = MailJob{Entry: *entry, ExpiresAt: ticket.ExpiresAt}
			return nil
		}
		paths, err := filepath.Glob(filepath.Join(s.root, operationsDir, "*"+jsonSuffix))
		if err != nil {
			return err
		}
		now = s.clock.Now().UTC()
		for _, path := range paths {
			if strings.HasPrefix(filepath.Base(path), claimPrefix) {
				continue
			}
			bundle, err := s.readBundlePathLocked(path)
			if err != nil {
				return err
			}
			entry := &bundle.Outbox
			if entry.DeliveryState == DeliverySending && entry.AttemptedAt != nil && now.Sub(*entry.AttemptedAt) >= staleAfter {
				entry.DeliveryState, entry.AttemptID, entry.AttemptedAt = DeliveryPending, "", nil
				entry.LastError = "previous sender exited before recording a result"
				bundle.addAudit("mail_attempt_recovered", "filees-mail", now)
			}
			if entry.DeliveryState != DeliveryPending {
				continue
			}
			if !now.Before(bundle.Operation.ExpiresAt) {
				entry.DeliveryState, entry.DeliveryAddress, entry.OTP = DeliveryFailed, "", ""
				entry.LastError = "operation expired before mail submission"
				bundle.addAudit("mail_expired", "filees-mail", now)
				if err := atomicWriteJSON(path, bundle); err != nil {
					return err
				}
				continue
			}
			attemptID, err := randomUUID(s.random)
			if err != nil {
				return err
			}
			entry.DeliveryState, entry.AttemptID, entry.AttemptedAt, entry.LastError = DeliverySending, attemptID, &now, ""
			entry.RetryCount++
			bundle.addAudit("mail_attempt_started", "filees-mail", now)
			if err := atomicWriteJSON(path, bundle); err != nil {
				return err
			}
			job = MailJob{Entry: *entry, ExpiresAt: bundle.Operation.ExpiresAt}
			return nil
		}
		return ErrNotFound
	})
	return job, err
}

func (s *Files) MarkOutboxQueued(messageID, attemptID string) error {
	if err := s.requireAreas(AreaTickets | AreaOperations | AreaAudit); err != nil {
		return err
	}
	return s.withLock(func() error {
		if path, ticket, err := s.findInvitationTicketLocked(messageID); err == nil {
			entry := &ticket.InvitationOutbox
			if entry.DeliveryState == DeliveryQueued {
				return nil
			}
			if entry.DeliveryState != DeliverySending || entry.AttemptID != attemptID {
				return ErrMailAttempt
			}
			now := s.clock.Now().UTC()
			entry.DeliveryState, entry.DeliveryAddress, entry.Invitation = DeliveryQueued, "", ""
			entry.AttemptID, entry.AttemptedAt, entry.LastError, entry.QueuedAt = "", nil, "", &now
			if err := atomicWriteJSON(path, ticket); err != nil {
				return err
			}
			return s.writeAuditLocked(AuditEvent{Event: "invitation_mail_queued", Actor: "filees-mail", ObjectType: "ticket", ObjectID: ticket.TicketID, At: now})
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		path, bundle, err := s.findBundleLocked(func(bundle Bundle) bool { return bundle.Outbox.MessageID == messageID })
		if err != nil {
			return err
		}
		if bundle.Outbox.DeliveryState == DeliveryQueued {
			return nil
		}
		if bundle.Outbox.DeliveryState != DeliverySending || bundle.Outbox.AttemptID != attemptID {
			return ErrMailAttempt
		}
		now := s.clock.Now().UTC()
		bundle.Outbox.DeliveryState, bundle.Outbox.DeliveryAddress, bundle.Outbox.OTP = DeliveryQueued, "", ""
		bundle.Outbox.AttemptID, bundle.Outbox.AttemptedAt, bundle.Outbox.LastError, bundle.Outbox.QueuedAt = "", nil, "", &now
		bundle.addAudit("mail_queued", "filees-mail", now)
		return atomicWriteJSON(path, bundle)
	})
}

func (s *Files) MarkOutboxFailed(messageID, attemptID, reason string, permanent bool) error {
	if err := s.requireAreas(AreaTickets | AreaOperations | AreaAudit); err != nil {
		return err
	}
	reason = strings.TrimSpace(reason)
	if len(reason) > 512 {
		reason = reason[:512]
	}
	if reason == "" {
		reason = "mail submission failed"
	}
	return s.withLock(func() error {
		if path, ticket, err := s.findInvitationTicketLocked(messageID); err == nil {
			entry := &ticket.InvitationOutbox
			if entry.DeliveryState != DeliverySending || entry.AttemptID != attemptID {
				return ErrMailAttempt
			}
			entry.DeliveryState, entry.AttemptID, entry.AttemptedAt, entry.LastError = DeliveryPending, "", nil, reason
			event := "invitation_mail_attempt_failed"
			if permanent {
				entry.DeliveryState, entry.DeliveryAddress, entry.Invitation = DeliveryFailed, "", ""
				event = "invitation_mail_failed"
			}
			now := s.clock.Now().UTC()
			if err := atomicWriteJSON(path, ticket); err != nil {
				return err
			}
			return s.writeAuditLocked(AuditEvent{Event: event, Actor: "filees-mail", ObjectType: "ticket", ObjectID: ticket.TicketID, At: now})
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		path, bundle, err := s.findBundleLocked(func(bundle Bundle) bool { return bundle.Outbox.MessageID == messageID })
		if err != nil {
			return err
		}
		entry := &bundle.Outbox
		if entry.DeliveryState != DeliverySending || entry.AttemptID != attemptID {
			return ErrMailAttempt
		}
		entry.DeliveryState, entry.AttemptID, entry.AttemptedAt, entry.LastError = DeliveryPending, "", nil, reason
		event := "mail_attempt_failed"
		if permanent {
			entry.DeliveryState, entry.DeliveryAddress, entry.OTP = DeliveryFailed, "", ""
			// A permanent OTP delivery failure must not hold the scarce reverse
			// tunnel port until the normal operation TTL elapses.
			bundle.Operation.State, bundle.Operation.OTPHash, bundle.Operation.OTPLocator, bundle.Operation.AttemptsLeft = OperationExpired, "", "", 0
			event = "mail_failed"
		}
		bundle.addAudit(event, "filees-mail", s.clock.Now().UTC())
		return atomicWriteJSON(path, bundle)
	})
}

func (s *Files) GetOperation(operationID string) (Operation, error) {
	if err := s.requireAreas(AreaOperations); err != nil {
		return Operation{}, err
	}
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
	if err := s.requireAreas(AreaOperations); err != nil {
		return nil, err
	}
	var entries []MailOutboxEntry
	err := s.withLock(func() error {
		bundles, err := s.readBundlesLocked()
		if err != nil {
			return err
		}
		for _, bundle := range bundles {
			// Mobile pairing operations (CreateMobilePairing) have no
			// e-mail/outbox step at all - their Bundle.Outbox is the zero
			// value, not a pending mail job to report or deliver.
			if bundle.Outbox.MessageID == "" {
				continue
			}
			entries = append(entries, bundle.Outbox)
		}
		return nil
	})
	return entries, err
}

func (s *Files) ListAudit() ([]AuditEvent, error) {
	if err := s.requireAreas(AreaOperations | AreaAudit); err != nil {
		return nil, err
	}
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
		case BundleSchema, legacyBundleSchemaV3, legacyBundleSchema:
			if err := decodeStrict(raw, header.Schema, &bundle); err != nil {
				return err
			}
			bundle.Schema = BundleSchema
			if bundle.Operation.Schema == legacyOperationSchema || bundle.Operation.Schema == legacyOperationSchemaV2 || bundle.Operation.Schema == legacyOperationSchemaV3 {
				bundle.Operation.Schema = OperationSchema
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
	clientID, err := randomUUID(s.random)
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
	op := Operation{Schema: OperationSchema, OperationID: operationID, ClientID: clientID, OnboardingRequestID: requestID, OTPHash: s.hashOTP(otp), OTPLocator: locator, ApprovedPolicy: ticket.ApprovedPolicy, AssignedReversePort: port, AttemptsLeft: s.otpAttempts, State: OperationAwaitingTunnel, CreatedAt: now, ExpiresAt: now.Add(s.operationTTL)}
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
		if now.Before(op.ExpiresAt) && (op.State == OperationAwaitingTunnel || op.State == OperationTunnelAuthorized || op.State == OperationTunnelStarted || op.State == OperationHelperAnnounced || op.State == OperationIdentityGenerated || op.State == OperationAccessStaged || op.State == OperationPossessionProved) {
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
		ticket, err := s.readTicketPathLocked(path)
		if err != nil {
			return nil, err
		}
		result[path] = ticket
	}
	return result, nil
}

func (s *Files) readTicketPathLocked(path string) (Ticket, error) {
	var ticket Ticket
	if err := readStrictJSON(path, TicketSchema, &ticket); err != nil {
		return Ticket{}, err
	}
	return ticket, nil
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
		if legacyErr := readStrictJSON(path, legacyBundleSchemaV3, &bundle); legacyErr != nil {
			if olderErr := readStrictJSON(path, legacyBundleSchema, &bundle); olderErr != nil {
				return Bundle{}, err
			}
		}
		bundle.Schema = BundleSchema
		if bundle.Operation.Schema == legacyOperationSchema || bundle.Operation.Schema == legacyOperationSchemaV2 || bundle.Operation.Schema == legacyOperationSchemaV3 {
			bundle.Operation.Schema = OperationSchema
		}
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

func (s *Files) findInvitationTicketLocked(messageID string) (string, Ticket, error) {
	paths, err := filepath.Glob(filepath.Join(s.root, ticketsDir, "*"+jsonSuffix))
	if err != nil {
		return "", Ticket{}, err
	}
	for _, path := range paths {
		ticket, err := s.readTicketPathLocked(path)
		if err != nil {
			return "", Ticket{}, err
		}
		if ticket.InvitationOutbox.MessageID == messageID {
			return path, ticket, nil
		}
	}
	return "", Ticket{}, ErrNotFound
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

// Privacy is asserted through privatefile rather than the permission bits.
// The mode test is exactly right on the OpenBSD server these paths live on,
// but it is meaningless anywhere Go reports 0666 for every writable file, so
// it made the whole package untestable on a Windows developer machine - 35
// tests refused before reaching a single assertion. privatefile keeps the
// mode-and-owner check on Unix and uses the DACL on Windows, so the guarantee
// is unchanged where it mattered and now also checkable where it did not.
func requirePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("onboarding directory %s must be a directory", path)
	}
	if err := privatefile.Verify(path); err != nil {
		return fmt.Errorf("onboarding directory %s must be private: %w", path, err)
	}
	return nil
}
func requirePrivateFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("onboarding file %s must be a regular file", path)
	}
	if err := privatefile.Verify(path); err != nil {
		return fmt.Errorf("onboarding file %s must be private: %w", path, err)
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
	switch policy.Kind {
	case "", KindDesktop, KindMobile:
	default:
		return errors.New("policy kind must be \"desktop\" or \"mobile\"")
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
	return TakeReceipt{OperationID: op.OperationID, OnboardingRequestID: op.OnboardingRequestID, AssignedReversePort: op.AssignedReversePort, ExpiresAt: op.ExpiresAt}
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

// ticketAddressTakenLocked reports whether an existing ticket file still has a
// claim on its delivery address.
//
// The guard this replaces asked only whether the file existed, so a ticket that
// had lapsed kept its address reserved forever and the only remedy was an
// administrator finding and deleting the file by hand - with nothing in the
// refusal hinting at that. A live ticket still blocks, because two valid
// invitations to one address would mean two usable capabilities for the same
// person.
//
// An unreadable or malformed file is treated as still blocking: it is not
// evidence that the address is free, and overwriting something we cannot parse
// would destroy whatever it was.
func (s *Files) ticketAddressTakenLocked(path string, now time.Time) (bool, error) {
	ticket, err := s.readTicketPathLocked(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, nil
	}
	return now.Before(ticket.ExpiresAt), nil
}
