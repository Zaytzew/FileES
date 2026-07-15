package onboarding

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

const testRealmID = "7b807185-aa75-4169-8a65-705c7cbab176"

func TestTakeIsAtomicIdempotentAndSeparatesEmail(t *testing.T) {
	now := time.Date(2026, 7, 15, 18, 0, 0, 0, time.UTC)
	store, path := openTestStore(t, &now, 4, 42000, 42010)
	ticket, err := store.CreateTicket("Alice@Example.COM", Policy{RealmID: testRealmID}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	requestID := uuid.NewString()
	receipt, err := store.Take("Alice@example.com", requestID)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.OnboardingRequestID != requestID || receipt.OperationID == "" {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}
	if tickets, err := store.ListTickets(); err != nil || len(tickets) != 0 {
		t.Fatalf("ticket must physically disappear: tickets=%v err=%v", tickets, err)
	}
	op, err := store.GetOperation(receipt.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(bytes.ToLower(raw), []byte("alice")) || bytes.Contains(raw, []byte("@")) {
		t.Fatalf("operation leaks delivery address: %s", raw)
	}
	entries, err := store.ListOutbox()
	if err != nil || len(entries) != 1 {
		t.Fatalf("outbox entries=%d err=%v", len(entries), err)
	}
	if entries[0].DeliveryAddress != "Alice@example.com" || entries[0].OTP == "" {
		t.Fatalf("outbox does not contain pending delivery material: %+v", entries[0])
	}

	retry, err := store.Take("Alice@example.com", requestID)
	if err != nil || retry != receipt {
		t.Fatalf("idempotent retry=%+v err=%v, want %+v", retry, err, receipt)
	}
	if entries, _ = store.ListOutbox(); len(entries) != 1 {
		t.Fatalf("retry created %d outbox entries", len(entries))
	}
	if _, err := store.Take("Alice@example.com", uuid.NewString()); !errors.Is(err, ErrTicketUnavailable) {
		t.Fatalf("new request after take error=%v", err)
	}

	job, err := store.ClaimPendingMail(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkOutboxQueued(job.Entry.MessageID, job.Entry.AttemptID); err != nil {
		t.Fatal(err)
	}
	entries, _ = store.ListOutbox()
	if entries[0].DeliveryAddress != "" || entries[0].OTP != "" || entries[0].DeliveryState != DeliveryQueued {
		t.Fatalf("delivered outbox retained secret material: %+v", entries[0])
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = reopenTestStore(t, path, &now, 4, 42000, 42010)
	t.Cleanup(func() { store.Close() })
	persisted, err := store.GetOperation(receipt.OperationID)
	if err != nil || persisted.OperationID != receipt.OperationID {
		t.Fatalf("operation did not survive restart: %+v err=%v", persisted, err)
	}
	events, err := store.ListAudit()
	if err != nil || len(events) < 2 {
		t.Fatalf("audit events=%d err=%v", len(events), err)
	}
	for _, event := range events {
		raw, _ := json.Marshal(event)
		if bytes.Contains(bytes.ToLower(raw), []byte("alice")) || bytes.Contains(raw, []byte("@")) || bytes.Contains(raw, []byte(entries[0].OTP)) && entries[0].OTP != "" {
			t.Fatalf("audit leaks secret/delivery data: %s", raw)
		}
	}
	_ = ticket
}

func TestConcurrentTakeConsumesTicketOnce(t *testing.T) {
	now := time.Date(2026, 7, 15, 18, 0, 0, 0, time.UTC)
	store, _ := openTestStore(t, &now, 3, 42100, 42120)
	defer store.Close()
	if _, err := store.CreateTicket("race@example.net", Policy{RealmID: testRealmID}, time.Hour); err != nil {
		t.Fatal(err)
	}
	const workers = 24
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	receipts := make(chan TakeReceipt, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			receipt, err := store.Take("race@example.net", uuid.NewString())
			if err == nil {
				receipts <- receipt
			} else {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(receipts)
	close(errs)
	if got := len(receipts); got != 1 {
		t.Fatalf("successful takes=%d, want 1", got)
	}
	for err := range errs {
		if !errors.Is(err, ErrTicketUnavailable) {
			t.Fatalf("losing take error=%v", err)
		}
	}
	entries, _ := store.ListOutbox()
	if len(entries) != 1 {
		t.Fatalf("outbox entries=%d, want 1", len(entries))
	}
}

func TestTakeFailureRollsBackAndExpiredTicketIsRemoved(t *testing.T) {
	now := time.Date(2026, 7, 15, 18, 0, 0, 0, time.UTC)
	store, _ := openTestStore(t, &now, 3, 42200, 42200)
	defer store.Close()
	ticket, err := store.CreateTicket("rollback@example.net", Policy{RealmID: testRealmID}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	store.random = errorReader{}
	if _, err := store.Take("rollback@example.net", uuid.NewString()); err == nil {
		t.Fatal("take unexpectedly succeeded with failed entropy source")
	}
	tickets, _ := store.ListTickets()
	if len(tickets) != 1 || tickets[0].TicketID != ticket.TicketID {
		t.Fatalf("failed transaction consumed ticket: %+v", tickets)
	}
	entries, _ := store.ListOutbox()
	if len(entries) != 0 {
		t.Fatalf("failed transaction created outbox: %+v", entries)
	}

	store.random = strings.NewReader(strings.Repeat("x", 256))
	now = now.Add(2 * time.Minute)
	if _, err := store.Take("rollback@example.net", uuid.NewString()); !errors.Is(err, ErrTicketUnavailable) {
		t.Fatalf("expired take error=%v", err)
	}
	tickets, _ = store.ListTickets()
	if len(tickets) != 0 {
		t.Fatalf("expired ticket was not physically removed: %+v", tickets)
	}
}

func TestMailAttemptRecoveryAndFailureRedaction(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	store, _ := openTestStore(t, &now, 3, 42220, 42230)
	defer store.Close()
	_, otp := createTakenOperation(t, store, "mail-failure@example.net")

	first, err := store.ClaimPendingMail(5 * time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(6 * time.Minute)
	second, err := store.ClaimPendingMail(5 * time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if second.Entry.AttemptID == first.Entry.AttemptID || second.Entry.RetryCount != 2 {
		t.Fatalf("stale attempt was not recovered: first=%+v second=%+v", first.Entry, second.Entry)
	}
	if err := store.MarkOutboxFailed(second.Entry.MessageID, second.Entry.AttemptID, "TLS connection reset", false); err != nil {
		t.Fatal(err)
	}
	entries, err := store.ListOutbox()
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary failure outbox=%+v err=%v", entries, err)
	}
	if entries[0].DeliveryState != DeliveryPending || entries[0].DeliveryAddress != "mail-failure@example.net" || entries[0].OTP != otp {
		t.Fatalf("temporary failure lost delivery material: %+v", entries[0])
	}

	third, err := store.ClaimPendingMail(5 * time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkOutboxFailed(third.Entry.MessageID, "foreign-attempt", "bad certificate", true); !errors.Is(err, ErrMailAttempt) {
		t.Fatalf("foreign attempt error=%v, want ErrMailAttempt", err)
	}
	if err := store.MarkOutboxFailed(third.Entry.MessageID, third.Entry.AttemptID, "bad certificate", true); err != nil {
		t.Fatal(err)
	}
	entries, _ = store.ListOutbox()
	if entries[0].DeliveryState != DeliveryFailed || entries[0].DeliveryAddress != "" || entries[0].OTP != "" {
		t.Fatalf("permanent failure retained delivery material: %+v", entries[0])
	}
}

func TestCrashAfterClaimRollsForwardOnRestart(t *testing.T) {
	if os.Getenv("FILEES_ONBOARDING_CRASH_HELPER") == "1" {
		crashTransactionHelper()
		return
	}
	for _, phase := range []string{"ticket_claim", "bundle_claim"} {
		t.Run(phase, func(t *testing.T) {
			now := time.Date(2026, 7, 15, 18, 0, 0, 0, time.UTC)
			store, path := openTestStore(t, &now, 3, 42250, 42260)
			if _, err := store.CreateTicket("crash@example.net", Policy{RealmID: testRealmID}, time.Hour); err != nil {
				t.Fatal(err)
			}
			ready := filepath.Join(t.TempDir(), "transaction-ready")
			requestID := uuid.NewString()
			command := exec.Command(os.Args[0], "-test.run=^TestCrashAfterClaimRollsForwardOnRestart$")
			command.Env = append(os.Environ(), "FILEES_ONBOARDING_CRASH_HELPER=1", "FILEES_ONBOARDING_CRASH_PHASE="+phase, "FILEES_ONBOARDING_ROOT="+path, "FILEES_ONBOARDING_REQUEST_ID="+requestID, "FILEES_ONBOARDING_READY="+ready)
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, err := os.Stat(ready); err == nil {
					break
				}
				if time.Now().After(deadline) {
					command.Process.Kill()
					command.Wait()
					t.Fatal("crash helper did not enter transaction")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err := command.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err := command.Wait(); err == nil {
				t.Fatal("crash helper unexpectedly exited cleanly")
			}
			store = reopenTestStore(t, path, &now, 3, 42250, 42260)
			defer store.Close()
			tickets, err := store.ListTickets()
			if err != nil || len(tickets) != 0 {
				t.Fatalf("recovered claim restored a consumed ticket: tickets=%+v err=%v", tickets, err)
			}
			receipt, err := store.Take("crash@example.net", requestID)
			if err != nil || receipt.OnboardingRequestID != requestID {
				t.Fatalf("claim did not roll forward idempotently: receipt=%+v err=%v", receipt, err)
			}
			claims, err := filepath.Glob(filepath.Join(path, operationsDir, claimPrefix+"*"))
			if err != nil || len(claims) != 0 {
				t.Fatalf("recovery left claims: %v err=%v", claims, err)
			}
		})
	}
}

func crashTransactionHelper() {
	now := time.Date(2026, 7, 15, 18, 0, 0, 0, time.UTC)
	store, err := Open(os.Getenv("FILEES_ONBOARDING_ROOT"), Options{Clock: ClockFunc(func() time.Time { return now }), OTPPepper: []byte(strings.Repeat("p", 32)), OperationTTL: 30 * time.Minute, OTPAttempts: 3, ReversePortFirst: 42250, ReversePortLast: 42260})
	if err != nil {
		os.Exit(2)
	}
	_ = store.withLock(func() error {
		entries, err := store.readTicketsLocked()
		if err != nil || len(entries) != 1 {
			os.Exit(3)
		}
		var ticketPath string
		var ticket Ticket
		for path, record := range entries {
			ticketPath = path
			ticket = record
		}
		claimPath := store.claimPath(os.Getenv("FILEES_ONBOARDING_REQUEST_ID"))
		if err := os.Rename(ticketPath, claimPath); err != nil {
			os.Exit(4)
		}
		if err := syncDirectory(filepath.Dir(ticketPath)); err != nil {
			os.Exit(5)
		}
		if err := syncDirectory(filepath.Dir(claimPath)); err != nil {
			os.Exit(6)
		}
		if os.Getenv("FILEES_ONBOARDING_CRASH_PHASE") == "bundle_claim" {
			bundle, err := store.bundleFromTicketLocked(ticket, os.Getenv("FILEES_ONBOARDING_REQUEST_ID"), now)
			if err != nil {
				os.Exit(7)
			}
			if err := atomicWriteJSON(claimPath, bundle); err != nil {
				os.Exit(8)
			}
		}
		if err := os.WriteFile(os.Getenv("FILEES_ONBOARDING_READY"), []byte("ready"), 0o600); err != nil {
			os.Exit(9)
		}
		select {}
	})
	os.Exit(10)
}

func TestPortExhaustionRollsBackSecondTicket(t *testing.T) {
	now := time.Date(2026, 7, 15, 18, 0, 0, 0, time.UTC)
	store, _ := openTestStore(t, &now, 3, 42300, 42300)
	defer store.Close()
	for _, address := range []string{"one@example.net", "two@example.net"} {
		if _, err := store.CreateTicket(address, Policy{RealmID: testRealmID}, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Take("one@example.net", uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Take("two@example.net", uuid.NewString()); !errors.Is(err, ErrNoReversePort) {
		t.Fatalf("second take error=%v", err)
	}
	tickets, _ := store.ListTickets()
	if len(tickets) != 1 || tickets[0].EmailDeliveryAddress != "two@example.net" {
		t.Fatalf("port exhaustion consumed second ticket: %+v", tickets)
	}
}

func TestOTPOneTimeAttemptsAndExpiry(t *testing.T) {
	t.Run("one time", func(t *testing.T) {
		now := time.Date(2026, 7, 15, 18, 0, 0, 0, time.UTC)
		store, _ := openTestStore(t, &now, 3, 42400, 42410)
		defer store.Close()
		receipt, otp := createTakenOperation(t, store, "otp@example.net")
		grant, err := store.AuthenticateOTP(strings.ToLower(otp))
		if err != nil || grant.OperationID != receipt.OperationID || grant.AssignedReversePort != 42400 {
			t.Fatalf("grant=%+v err=%v", grant, err)
		}
		if _, err := store.AuthenticateOTP(otp); !errors.Is(err, ErrOTPInvalid) {
			t.Fatalf("OTP reuse error=%v", err)
		}
		op, _ := store.GetOperation(receipt.OperationID)
		if op.OTPHash != "" || op.State != OperationTunnelAuthorized {
			t.Fatalf("authorized operation retains credential: %+v", op)
		}
	})

	t.Run("attempt limit persists", func(t *testing.T) {
		now := time.Date(2026, 7, 15, 18, 0, 0, 0, time.UTC)
		store, path := openTestStore(t, &now, 2, 42500, 42510)
		receipt, otp := createTakenOperation(t, store, "attempts@example.net")
		wrong := otp[:9] + strings.Repeat("A", 16)
		if wrong == otp {
			wrong = otp[:9] + strings.Repeat("B", 16)
		}
		if _, err := store.AuthenticateOTP(wrong); !errors.Is(err, ErrOTPInvalid) {
			t.Fatalf("wrong OTP error=%v", err)
		}
		op, _ := store.GetOperation(receipt.OperationID)
		if op.AttemptsLeft != 1 {
			t.Fatalf("attempts after failure=%d", op.AttemptsLeft)
		}
		store.Close()
		store = reopenTestStore(t, path, &now, 2, 42500, 42510)
		defer store.Close()
		if _, err := store.AuthenticateOTP(wrong); !errors.Is(err, ErrOTPInvalid) {
			t.Fatalf("last wrong OTP error=%v", err)
		}
		op, _ = store.GetOperation(receipt.OperationID)
		if op.AttemptsLeft != 0 || op.State != OperationOTPExhausted || op.OTPHash != "" {
			t.Fatalf("OTP was not exhausted durably: %+v", op)
		}
		if _, err := store.AuthenticateOTP(otp); !errors.Is(err, ErrOTPInvalid) {
			t.Fatalf("correct OTP after exhaustion error=%v", err)
		}
	})

	t.Run("expiry", func(t *testing.T) {
		now := time.Date(2026, 7, 15, 18, 0, 0, 0, time.UTC)
		store, _ := openTestStore(t, &now, 3, 42600, 42610)
		defer store.Close()
		receipt, otp := createTakenOperation(t, store, "expired@example.net")
		now = now.Add(31 * time.Minute)
		if _, err := store.AuthenticateOTP(otp); !errors.Is(err, ErrOTPExpired) {
			t.Fatalf("expired OTP error=%v", err)
		}
		op, _ := store.GetOperation(receipt.OperationID)
		if op.State != OperationExpired || op.OTPHash != "" {
			t.Fatalf("expiry was not persisted: %+v", op)
		}
	})
}

func TestTicketValidationRevocationAndPermissions(t *testing.T) {
	now := time.Date(2026, 7, 15, 18, 0, 0, 0, time.UTC)
	store, path := openTestStore(t, &now, 3, 42700, 42710)
	defer store.Close()
	for _, address := range []string{"Name <a@example.net>", "a@@example.net", ".a@example.net", "a@-example.net", "a b@example.net"} {
		if _, err := store.CreateTicket(address, Policy{RealmID: testRealmID}, time.Hour); err == nil {
			t.Errorf("invalid address %q accepted", address)
		}
	}
	if _, err := store.CreateTicket("a@example.net", Policy{RealmID: "not-a-uuid"}, time.Hour); err == nil {
		t.Fatal("invalid policy accepted")
	}
	ticket, err := store.CreateTicket("a@example.net", Policy{RealmID: testRealmID}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateTicket("a@EXAMPLE.NET", Policy{RealmID: testRealmID}, time.Hour); !errors.Is(err, ErrTicketExists) {
		t.Fatalf("duplicate ticket error=%v", err)
	}
	if err := store.RevokeTicket(ticket.TicketID); err != nil {
		t.Fatal(err)
	}
	if err := store.RevokeTicket(ticket.TicketID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second revoke error=%v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("toolchain root permissions=%04o, want 0700", got)
	}
	info, err = os.Stat(filepath.Join(path, lockName))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("toolchain lock permissions=%04o, want 0600", got)
	}
	if _, err := store.CreateTicket("record@example.net", Policy{RealmID: testRealmID}, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Take("record@example.net", uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	operations, err := filepath.Glob(filepath.Join(path, operationsDir, "*"+jsonSuffix))
	if err != nil || len(operations) != 1 {
		t.Fatalf("operation files=%v err=%v", operations, err)
	}
	info, err = os.Lstat(operations[0])
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("operation permissions=%04o, want 0600", got)
	}
}

func TestOpenRejectsUnsafeRootAndCleansInterruptedWrite(t *testing.T) {
	now := time.Date(2026, 7, 15, 18, 0, 0, 0, time.UTC)
	unsafe := filepath.Join(t.TempDir(), "unsafe")
	if err := os.Mkdir(unsafe, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(unsafe, testOptions(&now, 3, 42800, 42810)); err == nil {
		t.Fatal("world-readable root accepted")
	}

	store, root := openTestStore(t, &now, 3, 42800, 42810)
	stale := filepath.Join(root, operationsDir, ".write-interrupted")
	if err := os.WriteFile(stale, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = reopenTestStore(t, root, &now, 3, 42800, 42810)
	defer store.Close()
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("interrupted temporary write survived recovery: %v", err)
	}
}

func TestOpenExistingIsScopedAndDoesNotInitializeOrRecover(t *testing.T) {
	root := filepath.Join(t.TempDir(), "service")
	if _, err := OpenExisting(root, Options{}, Access{Areas: AreaTickets}); err == nil {
		t.Fatal("OpenExisting created or accepted a missing repository")
	}
	if err := Initialize(root); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(root, operationsDir, ".write-interrupted")
	if err := os.WriteFile(stale, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, auditDir)); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	opts := testOptions(&now, 3, 42800, 42810)
	opts.OTPPepper = nil
	store, err := OpenExisting(root, opts, Access{Areas: AreaTickets})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListTickets(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetOperation(uuid.NewString()); err == nil {
		t.Fatal("ticket-only handle accessed operations")
	}
	if err := store.Recover(); err == nil {
		t.Fatal("ticket-only handle performed recovery")
	}
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("OpenExisting unexpectedly recovered stale write: %v", err)
	}
}

func createTakenOperation(t *testing.T, store *Files, email string) (TakeReceipt, string) {
	t.Helper()
	if _, err := store.CreateTicket(email, Policy{RealmID: testRealmID}, time.Hour); err != nil {
		t.Fatal(err)
	}
	receipt, err := store.Take(email, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	entries, err := store.ListOutbox()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.OperationID == receipt.OperationID {
			return receipt, entry.OTP
		}
	}
	t.Fatal("operation has no outbox entry")
	return TakeReceipt{}, ""
}

func openTestStore(t *testing.T, now *time.Time, attempts int, first, last uint16) (*Files, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "onboarding")
	return reopenTestStore(t, path, now, attempts, first, last), path
}

func reopenTestStore(t *testing.T, path string, now *time.Time, attempts int, first, last uint16) *Files {
	t.Helper()
	store, err := Open(path, testOptions(now, attempts, first, last))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func testOptions(now *time.Time, attempts int, first, last uint16) Options {
	return Options{Clock: ClockFunc(func() time.Time { return *now }), OTPPepper: []byte(strings.Repeat("p", 32)), OperationTTL: 30 * time.Minute, OTPAttempts: attempts, ReversePortFirst: first, ReversePortLast: last}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, fmt.Errorf("injected entropy failure") }
