package onboarding

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// A demo server has no invitations: the only credential is the OTP mailed to
// the address the client names. What stops the endpoint from minting realms
// and mail without end is TakeDemo's admission, derived from the operations
// it already keeps (implementation notes (not distributed), portion 3).

const DemoOriginSchema = "filees.demo-origin/v1"

// DemoOrigin binds a demo operation to where it came from. Values are keyed
// hashes, never the address, the installation or the mailbox themselves: the
// list is kept for good and only ever needs equality.
type DemoOrigin struct {
	Schema    string    `json:"schema"`
	IPHash    string    `json:"ip_hash"`
	UIDHash   string    `json:"uid_hash"`
	EmailHash string    `json:"email_hash"`
	CreatedAt time.Time `json:"created_at"`
}

// DemoAdmission is the demo server's policy as the onboarding boundary needs it.
type DemoAdmission struct {
	RealmTTL time.Duration
	// IPBlock is how long an address stays refused after its realm expired.
	IPBlock time.Duration
	// RealmQuota is the space every live realm is entitled to.
	RealmQuota int64
	// ReapGrace covers the minute between a realm's TTL and the reap that
	// actually frees its space.
	ReapGrace time.Duration
	// FreeBytes reports free space on the repositories volume.
	FreeBytes func() (int64, error)
}

const (
	DemoRefusedInstallation = "demo_installation_used"
	DemoRefusedAddress      = "demo_address_cooling_down"
	DemoRefusedPending      = "demo_request_pending"
	DemoRefusedCapacity     = "demo_capacity"
)

// DemoRefusal is an admission answer, not a failure: the client shows it and,
// when RetryAfter is positive, when to try again. A demo server hides nothing
// behind an indistinguishable "accepted".
type DemoRefusal struct {
	Code       string
	RetryAfter time.Duration
}

func (r *DemoRefusal) Error() string { return "demo activation refused: " + r.Code }

// RetryAfterMinutes rounds up, so "try again in n minutes" is never early.
func (r *DemoRefusal) RetryAfterMinutes() int {
	if r.RetryAfter <= 0 {
		return 0
	}
	return int((r.RetryAfter + time.Minute - 1) / time.Minute)
}

func validDemoInstallationUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

// DemoClientAddress extracts the client address from the SSH_CONNECTION value
// sshd sets for a forced command. The client never supplies it.
func DemoClientAddress(sshConnection string) (string, error) {
	fields := strings.Fields(sshConnection)
	if len(fields) != 4 {
		return "", errors.New("SSH_CONNECTION is not set by sshd")
	}
	address, err := netip.ParseAddr(fields[0])
	if err != nil {
		return "", errors.New("SSH_CONNECTION client address is invalid")
	}
	return address.Unmap().String(), nil
}

func (s *Files) demoHash(kind, value string) string {
	mac := hmac.New(sha256.New, s.pepper)
	mac.Write([]byte("filees-demo/" + kind + "\x00" + value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// TakeDemo starts an invitation-less onboarding on a demo server. It refuses,
// in this order: an installation that has ever had a demo realm (for good), an
// address whose realm expired less than IPBlock ago or is still live, any of
// the three with a request still pending, and finally a server without room
// for one more full quota. Otherwise it mints a desktop operation for a new
// realm and queues the OTP mail exactly like a redeemed ticket.
func (s *Files) TakeDemo(email, installationUID, clientIP, requestID string, admission DemoAdmission) (TakeReceipt, error) {
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
	if !validDemoInstallationUID(installationUID) {
		return TakeReceipt{}, errors.New("installation_uid must be a UUID")
	}
	if _, err := netip.ParseAddr(clientIP); err != nil {
		return TakeReceipt{}, errors.New("demo client address is invalid")
	}
	if _, err := uuid.Parse(requestID); err != nil {
		return TakeReceipt{}, errors.New("onboarding_request_id must be a UUID")
	}
	if admission.RealmTTL <= 0 || admission.IPBlock < 0 || admission.RealmQuota <= 0 || admission.FreeBytes == nil {
		return TakeReceipt{}, errors.New("demo admission is incomplete")
	}
	origin := DemoOrigin{
		Schema: DemoOriginSchema, IPHash: s.demoHash("ip", clientIP),
		UIDHash: s.demoHash("uid", installationUID), EmailHash: s.demoHash("email", canonical),
	}
	var receipt TakeReceipt
	err = s.withLock(func() error {
		if err := s.recoverClaimsLocked(); err != nil {
			return err
		}
		if bundle, err := s.readBundlePathLocked(s.operationPath(requestID)); err == nil {
			if bundle.DemoOrigin == nil || *bundle.DemoOrigin != withCreatedAt(origin, bundle.DemoOrigin.CreatedAt) {
				return ErrRequestConflict
			}
			receipt = receiptFor(bundle.Operation)
			return nil
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		now := s.clock.Now().UTC()
		if refusal, err := s.demoAdmitLocked(origin, admission, now); err != nil {
			return err
		} else if refusal != nil {
			return refusal
		}
		ticketID, err := randomUUID(s.random)
		if err != nil {
			return err
		}
		ticket := Ticket{Schema: TicketSchema, TicketID: ticketID, EmailDeliveryAddress: canonical, ApprovedPolicy: Policy{Kind: KindDesktop}, CreatedAt: now, ExpiresAt: now.Add(s.operationTTL)}
		bundle, err := s.bundleFromTicketLocked(ticket, requestID, now)
		if err != nil {
			return err
		}
		origin.CreatedAt = now
		bundle.DemoOrigin = &origin
		bundle.addAudit("demo_onboarding_started", "filees-onboard", now)
		if err := atomicWriteJSON(s.operationPath(requestID), bundle); err != nil {
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

func withCreatedAt(origin DemoOrigin, at time.Time) DemoOrigin {
	origin.CreatedAt = at
	return origin
}

func (s *Files) demoAdmitLocked(origin DemoOrigin, admission DemoAdmission, now time.Time) (*DemoRefusal, error) {
	paths, err := filepath.Glob(filepath.Join(s.root, operationsDir, "*"+jsonSuffix))
	if err != nil {
		return nil, err
	}
	var refusal *DemoRefusal
	refuse := func(code string, until time.Time) {
		candidate := &DemoRefusal{Code: code, RetryAfter: until.Sub(now)}
		if code == DemoRefusedInstallation {
			candidate.RetryAfter = 0
		}
		// The installation refusal is final and wins; otherwise the longest
		// wait is the honest one.
		if refusal == nil || candidate.Code == DemoRefusedInstallation || (refusal.Code != DemoRefusedInstallation && candidate.RetryAfter > refusal.RetryAfter) {
			refusal = candidate
		}
	}
	live := 0
	var nextFree time.Time
	for _, path := range paths {
		if strings.HasPrefix(filepath.Base(path), claimPrefix) {
			continue
		}
		bundle, err := s.readBundlePathLocked(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, err
		}
		if bundle.DemoOrigin == nil {
			continue
		}
		op, other := bundle.Operation, bundle.DemoOrigin
		var occupiedUntil time.Time
		switch {
		case op.ActivatedAt != nil:
			realmEnd := op.ActivatedAt.UTC().Add(admission.RealmTTL)
			if other.UIDHash == origin.UIDHash {
				refuse(DemoRefusedInstallation, time.Time{})
			}
			if other.IPHash == origin.IPHash && now.Before(realmEnd.Add(admission.IPBlock)) {
				refuse(DemoRefusedAddress, realmEnd.Add(admission.IPBlock))
			}
			occupiedUntil = realmEnd.Add(admission.ReapGrace)
		case now.Before(op.ExpiresAt) && op.State != OperationExpired && op.State != OperationOTPExhausted:
			if other.UIDHash == origin.UIDHash || other.IPHash == origin.IPHash || other.EmailHash == origin.EmailHash {
				refuse(DemoRefusedPending, op.ExpiresAt)
			}
			// A pending request may still become a realm; it holds its quota.
			occupiedUntil = op.ExpiresAt
		}
		if now.Before(occupiedUntil) {
			live++
			if nextFree.IsZero() || occupiedUntil.Before(nextFree) {
				nextFree = occupiedUntil
			}
		}
	}
	if refusal != nil {
		return refusal, nil
	}
	free, err := admission.FreeBytes()
	if err != nil {
		return nil, err
	}
	if free < int64(live+1)*admission.RealmQuota {
		until := nextFree
		if until.IsZero() {
			// Nothing of ours holds the space; the volume is simply full.
			until = now.Add(time.Hour)
		}
		return &DemoRefusal{Code: DemoRefusedCapacity, RetryAfter: until.Sub(now)}, nil
	}
	return nil, nil
}
