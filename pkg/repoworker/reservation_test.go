package repoworker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestReservationLedgerAccountsForConcurrentOperationsAndReclaimsExpiry(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	capacity := &fakeCapacity{available: 1000, required: 600}
	ledger := &FileReservationLedger{Root: t.TempDir(), Capacity: capacity, TTL: time.Minute}
	first := uuid.NewString()
	available, required, expires, err := ledger.Reserve(context.Background(), first, 100, now)
	if err != nil || available != 1000 || required != 600 || !expires.Equal(now.Add(time.Minute)) {
		t.Fatalf("first reservation: available=%d required=%d expires=%v err=%v", available, required, expires, err)
	}
	if _, _, _, err := ledger.Reserve(context.Background(), uuid.NewString(), 100, now); !errors.Is(err, ErrReservationUnavailable) {
		t.Fatalf("overlapping reservation err=%v", err)
	}
	if _, _, _, err := ledger.Reserve(context.Background(), uuid.NewString(), 100, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("expired reservation was not reclaimed: %v", err)
	}
}

func TestReservationLedgerRenewsExpiredOperationAndReleases(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	opID := uuid.NewString()
	ledger := &FileReservationLedger{Root: t.TempDir(), Capacity: &fakeCapacity{available: 1000, required: 300}, TTL: time.Minute}
	if _, _, _, err := ledger.Reserve(context.Background(), opID, 100, now); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Ensure(context.Background(), opID, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("renew expired reservation: %v", err)
	}
	if err := ledger.Release(opID); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Ensure(context.Background(), opID, now); !errors.Is(err, ErrReservationUnavailable) {
		t.Fatalf("released reservation err=%v", err)
	}
}

func TestReservationLedgerEnsureRechecksCurrentCapacity(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	opID := uuid.NewString()
	capacity := &fakeCapacity{available: 1000, required: 300}
	ledger := &FileReservationLedger{Root: t.TempDir(), Capacity: capacity, TTL: time.Hour}
	if _, _, _, err := ledger.Reserve(context.Background(), opID, 100, now); err != nil {
		t.Fatal(err)
	}
	capacity.available = 200
	if err := ledger.Ensure(context.Background(), opID, now.Add(time.Minute)); !errors.Is(err, ErrReservationUnavailable) {
		t.Fatalf("capacity loss was accepted: %v", err)
	}
}
