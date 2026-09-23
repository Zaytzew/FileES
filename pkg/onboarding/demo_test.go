//go:build !windows

package onboarding

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type demoFixture struct {
	t     *testing.T
	store *Files
	now   *time.Time
	free  int64
}

func newDemoFixture(t *testing.T) *demoFixture {
	t.Helper()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	store, _ := openTestStore(t, &now, 3, 43000, 43100)
	return &demoFixture{t: t, store: store, now: &now, free: 100 << 30}
}

func (f *demoFixture) admission() DemoAdmission {
	return DemoAdmission{RealmTTL: 2 * time.Hour, IPBlock: 24 * time.Hour, RealmQuota: 1 << 30, ReapGrace: time.Minute, FreeBytes: func() (int64, error) { return f.free, nil }}
}

func (f *demoFixture) take(email, uid, ip string) (TakeReceipt, *DemoRefusal) {
	f.t.Helper()
	receipt, err := f.store.TakeDemo(email, uid, ip, uuid.NewString(), f.admission())
	var refusal *DemoRefusal
	if errors.As(err, &refusal) {
		return receipt, refusal
	}
	if err != nil {
		f.t.Fatal(err)
	}
	return receipt, nil
}

// activate stands in for the tunnel, proof and publish chain, which is covered
// elsewhere; admission only reads when an operation became active.
func (f *demoFixture) activate(receipt TakeReceipt) {
	f.t.Helper()
	path := f.store.operationPath(receipt.OnboardingRequestID)
	bundle, err := f.store.readBundlePathLocked(path)
	if err != nil {
		f.t.Fatal(err)
	}
	at := *f.now
	bundle.Operation.State, bundle.Operation.ActivatedAt = OperationActive, &at
	if err := atomicWriteJSON(path, bundle); err != nil {
		f.t.Fatal(err)
	}
}

func wantRefusal(t *testing.T, what string, refusal *DemoRefusal, code string, minutes int) {
	t.Helper()
	if refusal == nil || refusal.Code != code || refusal.RetryAfterMinutes() != minutes {
		t.Fatalf("%s: refusal=%+v minutes=%d, want %s after %d", what, refusal, refusalMinutes(refusal), code, minutes)
	}
}

func refusalMinutes(refusal *DemoRefusal) int {
	if refusal == nil {
		return -1
	}
	return refusal.RetryAfterMinutes()
}

func TestDemoTakeMintsAnOTPForANewRealmWithoutAnyTicket(t *testing.T) {
	f := newDemoFixture(t)
	uid := uuid.NewString()
	receipt, refusal := f.take("reviewer@example.test", uid, "203.0.113.7")
	if refusal != nil || receipt.OperationID == "" || receipt.AssignedReversePort == 0 {
		t.Fatalf("receipt=%+v refusal=%+v", receipt, refusal)
	}
	op, err := f.store.GetOperation(receipt.OperationID)
	if err != nil || op.ApprovedPolicy.RealmID != "" || op.ApprovedPolicy.Kind != KindDesktop {
		t.Fatalf("operation=%+v err=%v, want an unbound desktop operation", op, err)
	}
	outbox, err := f.store.ListOutbox()
	if err != nil || len(outbox) != 1 || outbox[0].OTP == "" || outbox[0].DeliveryAddress != "reviewer@example.test" {
		t.Fatalf("outbox=%+v err=%v", outbox, err)
	}
	raw, err := os.ReadFile(f.store.operationPath(receipt.OnboardingRequestID))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"203.0.113.7", uid} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("demo origin stored %q in the clear", secret)
		}
	}
	grant, err := f.store.AuthenticateOTP(outbox[0].OTP)
	if err != nil || grant.ApprovedPolicy.RealmID == "" {
		t.Fatalf("OTP grant=%+v err=%v, want a fresh realm", grant, err)
	}
}

func TestDemoAdmissionFreezesInstallationForeverAndAddressForADay(t *testing.T) {
	f := newDemoFixture(t)
	uid := uuid.NewString()
	receipt, _ := f.take("one@example.test", uid, "203.0.113.7")

	// While pending, the same address, installation or mailbox waits for it.
	_, refusal := f.take("two@example.test", uuid.NewString(), "203.0.113.7")
	wantRefusal(t, "pending address", refusal, DemoRefusedPending, 30)
	_, refusal = f.take("one@example.test", uuid.NewString(), "198.51.100.1")
	wantRefusal(t, "pending mailbox", refusal, DemoRefusedPending, 30)

	f.activate(receipt)
	*f.now = f.now.Add(10 * time.Minute)
	_, refusal = f.take("three@example.test", uuid.NewString(), "203.0.113.7")
	// Realm ends at +2h from activation, then the address cools for 24h.
	wantRefusal(t, "live realm address", refusal, DemoRefusedAddress, 26*60-10)
	_, refusal = f.take("four@example.test", uid, "198.51.100.2")
	wantRefusal(t, "used installation", refusal, DemoRefusedInstallation, 0)

	*f.now = f.now.Add(26*time.Hour + time.Minute)
	if _, refusal = f.take("five@example.test", uuid.NewString(), "203.0.113.7"); refusal != nil {
		t.Fatalf("address still refused a day after expiry: %+v", refusal)
	}
	_, refusal = f.take("six@example.test", uid, "198.51.100.3")
	wantRefusal(t, "installation after a day", refusal, DemoRefusedInstallation, 0)
}

func TestDemoCapacityNamesWhenTheNearestRealmFreesItsSpace(t *testing.T) {
	f := newDemoFixture(t)
	f.free = 2<<30 + 1
	first, _ := f.take("a@example.test", uuid.NewString(), "203.0.113.10")
	f.activate(first)
	*f.now = f.now.Add(20 * time.Minute)
	// One live realm holds a quota; the new one needs a second. Space for two.
	second, refusal := f.take("b@example.test", uuid.NewString(), "203.0.113.11")
	if refusal != nil {
		t.Fatalf("room for two refused: %+v", refusal)
	}
	f.activate(second)
	*f.now = f.now.Add(10 * time.Minute)
	_, refusal = f.take("c@example.test", uuid.NewString(), "203.0.113.12")
	// The first realm ends 2h after its activation, 30 minutes ago, plus the
	// reap minute.
	wantRefusal(t, "capacity", refusal, DemoRefusedCapacity, 91)

	*f.now = f.now.Add(91 * time.Minute)
	if _, refusal = f.take("c@example.test", uuid.NewString(), "203.0.113.12"); refusal != nil {
		t.Fatalf("refused after the nearest realm freed its quota: %+v", refusal)
	}
}

func TestDemoRequestProtocolIsClosed(t *testing.T) {
	uid := uuid.NewString()
	valid := `{"schema":"filees.onboard-request/demo-v1","email":"r@example.test","installation_uid":"` + uid + `","onboarding_request_id":"` + uuid.NewString() + `"}`
	if request, err := DecodeOnboardRequest(strings.NewReader(valid)); err != nil || request.InstallationUID != uid {
		t.Fatalf("request=%+v err=%v", request, err)
	}
	for name, raw := range map[string]string{
		"missing uid":       `{"schema":"filees.onboard-request/demo-v1","email":"r@example.test","onboarding_request_id":"` + uuid.NewString() + `"}`,
		"uid not canonical": `{"schema":"filees.onboard-request/demo-v1","email":"r@example.test","installation_uid":"` + strings.ToUpper(uid) + `","onboarding_request_id":"` + uuid.NewString() + `"}`,
		"invitation mixed":  `{"schema":"filees.onboard-request/demo-v1","email":"r@example.test","installation_uid":"` + uid + `","invitation_token":"x","onboarding_request_id":"` + uuid.NewString() + `"}`,
		"uid on legacy":     `{"schema":"filees.onboard-request/v1","email":"r@example.test","installation_uid":"` + uid + `","onboarding_request_id":"` + uuid.NewString() + `"}`,
		"client address":    `{"schema":"filees.onboard-request/demo-v1","email":"r@example.test","installation_uid":"` + uid + `","client_ip":"1.2.3.4","onboarding_request_id":"` + uuid.NewString() + `"}`,
	} {
		if _, err := DecodeOnboardRequest(strings.NewReader(raw)); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	var response OnboardResponse
	if err := json.Unmarshal(EncodeDemoRefusal("id", &DemoRefusal{Code: DemoRefusedCapacity, RetryAfter: 90*time.Second + time.Nanosecond}), &response); err != nil || response.Status != DemoRefusedCapacity || response.RetryAfterMinutes != 2 {
		t.Fatalf("refusal response=%+v err=%v", response, err)
	}
	if address, err := DemoClientAddress("::ffff:203.0.113.7 51234 202.61.192.51 22"); err != nil || address != "203.0.113.7" {
		t.Fatalf("address=%q err=%v", address, err)
	}
	if _, err := DemoClientAddress(""); err == nil {
		t.Fatal("missing SSH_CONNECTION accepted")
	}
}

// The privacy policy promises that nothing of a demo activation is left on
// the demo server 14 days after it ended. An activated demo ends when its
// realm's TTL runs out, one never activated when its OTP window closes;
// anything that did not come from the demo endpoint is never touched.
func TestFinishedDemoRecordsAreDeletedAfterTheRetention(t *testing.T) {
	f := newDemoFixture(t)
	uid := uuid.NewString()
	activated, refusal := f.take("a@example.net", uid, "198.51.100.7")
	if refusal != nil {
		t.Fatalf("first demo refused: %+v", refusal)
	}
	f.activate(activated)
	pending, refusal := f.take("b@example.net", uuid.NewString(), "198.51.100.8")
	if refusal != nil {
		t.Fatalf("second demo refused: %+v", refusal)
	}
	// An ordinary operation shares the directory and must survive.
	ordinaryPath := f.store.operationPath(uuid.NewString())
	bundle, err := f.store.readBundlePathLocked(f.store.operationPath(pending.OnboardingRequestID))
	if err != nil {
		t.Fatal(err)
	}
	bundle.DemoOrigin = nil
	if err := atomicWriteJSON(ordinaryPath, bundle); err != nil {
		t.Fatal(err)
	}
	ttl := f.admission().RealmTTL

	*f.now = f.now.Add(ttl + DemoRecordRetention - time.Hour)
	if deleted, err := f.store.PruneDemoRecords(ttl, DemoRecordRetention); err != nil || deleted != 1 {
		t.Fatalf("an hour before the activated one's retention ends: deleted %d, %v (only the never-activated one may go)", deleted, err)
	}
	if _, err := os.Stat(f.store.operationPath(activated.OnboardingRequestID)); err != nil {
		t.Fatalf("the activated demo was deleted before its retention ended: %v", err)
	}
	if _, refusal := f.take("c@example.net", uid, "203.0.113.9"); refusal == nil || refusal.Code != DemoRefusedInstallation {
		t.Fatalf("within the retention the installation must still be refused: %+v", refusal)
	}

	*f.now = f.now.Add(2 * time.Hour)
	if deleted, err := f.store.PruneDemoRecords(ttl, DemoRecordRetention); err != nil || deleted != 1 {
		t.Fatalf("after the retention: deleted %d, %v", deleted, err)
	}
	if _, err := os.Stat(f.store.operationPath(activated.OnboardingRequestID)); !os.IsNotExist(err) {
		t.Fatalf("the activated demo survived its retention: %v", err)
	}
	if _, err := os.Stat(ordinaryPath); err != nil {
		t.Fatalf("an operation that did not come from the demo endpoint was deleted: %v", err)
	}
	// Nothing of the first activation is left, so the same installation may
	// try the demo again - the owner accepted this.
	if _, refusal := f.take("a@example.net", uid, "198.51.100.7"); refusal != nil {
		t.Fatalf("after the retention the installation is refused: %+v", refusal)
	}
}
