package recipientotp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"filees/pkg/repoworker"
	"github.com/google/uuid"
)

func (f fixture) sends(t *testing.T) int {
	t.Helper()
	budget, err := f.service.loadBudget(f.storeID(t))
	if err != nil {
		t.Fatal(err)
	}
	return len(budget.Sends)
}

// Default policy: 5 queue attempts per invitation in 24 h, pauses of
// 30 s, 2 min, then 10 min between them, counted across OTP epochs and
// restarts, with the window sliding rather than resetting.
func TestOTPSendBudgetPerInvitation(t *testing.T) {
	f := newFixture(t)
	request := Request{Alias: "realm", Slug: "files", Invitation: f.invitation}
	start := *f.now
	step := func(after time.Duration, wantErr error, wantSends int) {
		t.Helper()
		*f.now = f.now.Add(after)
		// A fresh Service value is a restart: nothing is kept in memory.
		service := f.service
		if err := service.RequestCode(request); !errors.Is(err, wantErr) {
			t.Fatalf("at +%s: err=%v, want %v", f.now.Sub(start), err, wantErr)
		}
		if got := f.sends(t); got != wantSends {
			t.Fatalf("at +%s: sends=%d, want %d", f.now.Sub(start), got, wantSends)
		}
	}
	step(0, nil, 1)
	step(10*time.Second, nil, 1) // cooling: silent, nothing mailed
	step(20*time.Second, nil, 2) // 30 s after the first
	step(time.Minute, nil, 2)    // cooling: 2 min after the second
	step(time.Minute, nil, 3)
	step(5*time.Minute, nil, 3) // cooling: 10 min from the third on; the OTP epoch expired meanwhile
	step(5*time.Minute, nil, 4)
	step(10*time.Minute, nil, 5)
	step(10*time.Minute, ErrSendBudget, 5) // five in the window
	step(12*time.Hour, ErrSendBudget, 5)
	*f.now = start.Add(budgetWindow)
	step(0, nil, 5) // the first send left the window: one more fits, total stays five
}

func TestOTPSendBudgetPerChannel(t *testing.T) {
	f := newFixture(t)
	f.service.SendsPerInvitation = 10
	f.service.SendsPerChannel = 2
	request := Request{Alias: "realm", Slug: "files", Invitation: f.invitation}
	if err := f.service.RequestCode(request); err != nil {
		t.Fatal(err)
	}
	*f.now = f.now.Add(time.Minute)
	if err := f.service.RequestCode(request); err != nil {
		t.Fatal(err)
	}
	*f.now = f.now.Add(time.Hour)
	if err := f.service.RequestCode(request); !errors.Is(err, ErrSendBudget) {
		t.Fatalf("channel ceiling not enforced: %v", err)
	}
}

func TestOTPSendBudgetDefaults(t *testing.T) {
	var s Service
	if s.sendsPerInvitation() != 5 || s.sendsPerChannel() != 50 {
		t.Fatalf("defaults = %d/%d", s.sendsPerInvitation(), s.sendsPerChannel())
	}
}

func TestOTPBudgetRejectsInvalidServiceLimits(t *testing.T) {
	f := newFixture(t)
	for _, values := range [][2]int{{-1, 0}, {0, -1}, {1001, 0}, {0, maxBudgetEntries + 1}} {
		service := f.service
		service.SendsPerInvitation, service.SendsPerChannel = values[0], values[1]
		if err := service.validate(); err == nil {
			t.Fatalf("invalid service limits accepted: %v", values)
		}
	}
}

func TestOTPBudgetsAcrossInvitationsAndConcurrentServices(t *testing.T) {
	if !repoworker.FileLocksSupported() {
		t.Skip("server concurrency requires kernel locks")
	}
	f := newFixture(t)
	updated := f.share
	updated.Recipients = []string{"a@example.test", "b@example.test"}
	_, deliveries, err := f.store.Update(uuid.NewString(), f.owner, f.storeID(t), updated)
	if err != nil || len(deliveries) != 2 {
		t.Fatalf("two invitations: %v %v", deliveries, err)
	}
	f.service.SendsPerChannel = 1
	results := make(chan error, 2)
	start := make(chan struct{})
	for _, delivery := range deliveries {
		go func(invitation string) {
			<-start
			service := f.service // independent services, same persistent root
			results <- service.RequestCode(Request{Alias: "realm", Slug: "files", Invitation: invitation})
		}(delivery.Token)
	}
	close(start)
	accepted, exhausted := 0, 0
	var unexpected error
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, ErrSendBudget):
			exhausted++
		default:
			unexpected = errors.Join(unexpected, err)
		}
	}
	if unexpected != nil {
		t.Fatal(unexpected)
	}
	if accepted != 1 || exhausted != 1 || f.sends(t) != 1 {
		t.Fatalf("channel race: accepted=%d exhausted=%d sends=%d", accepted, exhausted, f.sends(t))
	}
}

func TestOTPBudgetReservesBeforeOutboxAndSurvivesFailure(t *testing.T) {
	f := newFixture(t)
	// A regular file instead of an outbox directory makes enqueue fail.
	if err := os.WriteFile(f.service.Outbox.Root, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	request := Request{Alias: "realm", Slug: "files", Invitation: f.invitation}
	if err := f.service.RequestCode(request); err == nil || f.sends(t) != 1 {
		t.Fatalf("failed enqueue did not retain reservation: %v", err)
	}
	if err := os.Remove(f.service.Outbox.Root); err != nil {
		t.Fatal(err)
	}
	service := f.service
	if err := service.RequestCode(request); err != nil || f.sends(t) != 1 {
		t.Fatalf("restart lost cooldown: %v", err)
	}
	if _, ok, err := service.Outbox.Claim(*f.now, time.Minute); err != nil || ok {
		t.Fatalf("cooldown queued a mail: ok=%v err=%v", ok, err)
	}
	*f.now = f.now.Add(DefaultCooldown)
	if err := service.RequestCode(request); err != nil || f.sends(t) != 2 {
		t.Fatalf("retry after cooldown: %v", err)
	}
}

func TestOTPBudgetWriteFailureCannotQueueMail(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix directory permissions fixture")
	}
	f := newFixture(t)
	request := Request{Alias: "realm", Slug: "files", Invitation: f.invitation}
	id := f.storeID(t)
	_, _, digest, err := f.service.recipient(request)
	if err != nil {
		t.Fatal(err)
	}
	// An existing epoch avoids a prior state write masking the budget failure.
	if err := f.service.store(state{Schema: stateSchema, ChannelID: id, InvitationHash: digest, Epoch: uuid.NewString(), ActivatedAt: *f.now, ExpiresAt: f.now.Add(DefaultTTL)}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(f.service.Root, id)
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
	probe, err := os.CreateTemp(dir, "probe-")
	if err == nil {
		probe.Close()
		os.Remove(probe.Name())
		t.Skip("privileged user bypasses directory write protection")
	}
	if err := f.service.RequestCode(request); err == nil {
		t.Fatal("unwritable budget accepted")
	}
	if _, ok, err := f.service.Outbox.Claim(*f.now, time.Minute); err != nil || ok {
		t.Fatalf("budget failure queued mail: %v %v", ok, err)
	}
}

func TestOTPBudgetStorageAcceptsMaximumAndRejectsExcess(t *testing.T) {
	f := newFixture(t)
	id := f.storeID(t)
	budget := sendBudget{Schema: budgetSchema, ChannelID: id, Sends: make([]budgetEntry, maxBudgetEntries)}
	for i := range budget.Sends {
		budget.Sends[i] = budgetEntry{InvitationHash: strings.Repeat("a", 64), At: f.now.Add(time.Nanosecond)}
	}
	if err := f.service.storeBudget(budget); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(f.service.budgetPath(id))
	if err != nil || info.Size() <= 64<<10 || info.Size() > maxBudgetBytes {
		t.Fatalf("boundary fixture size: %v %v", info, err)
	}
	loaded, err := f.service.loadBudget(id)
	if err != nil || len(loaded.Sends) != maxBudgetEntries {
		t.Fatalf("maximum budget cannot round-trip: %d %v", len(loaded.Sends), err)
	}
	budget.Sends = append(budget.Sends, budget.Sends[0])
	if err := f.service.storeBudget(budget); err == nil {
		t.Fatal("too many budget entries accepted")
	}
	if kept, err := f.service.loadBudget(id); err != nil || len(kept.Sends) != maxBudgetEntries {
		t.Fatal("invalid write damaged previous state", err)
	}
	// A sparse oversized file exercises the read bound without a large fixture.
	file, err := os.OpenFile(f.service.budgetPath(id), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	err = file.Truncate(maxBudgetBytes + 1)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.loadBudget(id); err == nil {
		t.Fatal("oversized budget accepted")
	}
}

func TestOTPBudgetInvalidStateFailsClosed(t *testing.T) {
	for _, corruption := range []string{"json", "channel", "hash", "time"} {
		t.Run(corruption, func(t *testing.T) {
			f := newFixture(t)
			id := f.storeID(t)
			budget := sendBudget{Schema: budgetSchema, ChannelID: id, Sends: []budgetEntry{{InvitationHash: strings.Repeat("a", 64), At: *f.now}}}
			switch corruption {
			case "channel":
				budget.ChannelID = uuid.NewString()
			case "hash":
				budget.Sends[0].InvitationHash = "garbage"
			case "time":
				budget.Sends[0].At = time.Time{}
			}
			raw, _ := json.Marshal(budget)
			if corruption == "json" {
				raw = []byte("{")
			}
			if err := os.MkdirAll(filepath.Join(f.service.Root, id), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(f.service.budgetPath(id), raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err := f.service.RequestCode(Request{Alias: "realm", Slug: "files", Invitation: f.invitation}); err == nil {
				t.Fatal("invalid state permitted send")
			}
			if _, ok, err := f.service.Outbox.Claim(*f.now, time.Minute); err != nil || ok {
				t.Fatalf("invalid state queued mail: %v %v", ok, err)
			}
		})
	}
}

func TestOTPBudgetSweepPersistsPartialPruneAndKeepsFuture(t *testing.T) {
	f := newFixture(t)
	id := f.storeID(t)
	digest := strings.Repeat("a", 64)
	budget := sendBudget{Schema: budgetSchema, ChannelID: id, Sends: []budgetEntry{
		{InvitationHash: digest, At: f.now.Add(-budgetWindow)},
		{InvitationHash: digest, At: f.now.Add(-time.Hour)},
		{InvitationHash: digest, At: f.now.Add(time.Hour)},
	}}
	if err := f.service.storeBudget(budget); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(filepath.Join(f.service.Root, id))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := f.service.sweepBudget(root, id, *f.now); err != nil {
		t.Fatal(err)
	}
	budget, err = f.service.loadBudget(id)
	if err != nil || len(budget.Sends) != 2 {
		t.Fatalf("partial prune not persisted: %d %v", len(budget.Sends), err)
	}
	if budget.decide(digest, *f.now, 5, 50, DefaultCooldown) != budgetCooling {
		t.Fatal("clock rollback did not preserve cooldown")
	}
	if err := f.service.sweepBudget(root, id, f.now.Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Stat(budgetName); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("fully expired budget survived", err)
	}
}

func TestOTPBudgetRecipientRemovalCannotResetChannelQuota(t *testing.T) {
	f := newFixture(t)
	f.service.SendsPerChannel = 1
	if err := f.service.RequestCode(Request{Alias: "realm", Slug: "files", Invitation: f.invitation}); err != nil {
		t.Fatal(err)
	}
	updated := f.share
	updated.Recipients = []string{"new@example.test"}
	_, deliveries, err := f.store.Update(uuid.NewString(), f.owner, f.storeID(t), updated)
	if err != nil || len(deliveries) != 1 {
		t.Fatalf("replace recipient: %v %v", deliveries, err)
	}
	if _, err := f.service.Sweep(context.Background(), *f.now); err != nil {
		// Windows does not implement maintenance try-lock.
		if runtime.GOOS != "windows" {
			t.Fatal(err)
		}
	}
	if err := f.service.RequestCode(Request{Alias: "realm", Slug: "files", Invitation: deliveries[0].Token}); !errors.Is(err, ErrSendBudget) {
		t.Fatalf("replacing recipient reset quota: %v", err)
	}
}
