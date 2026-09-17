package serverconfig

import (
	"strings"
	"testing"
	"time"
)

func TestDemoSectionIsTheWholePolicyOrNothing(t *testing.T) {
	zero, seven := 0, 7
	cases := []struct {
		name      string
		file      File
		wantTTL   time.Duration
		wantError string
	}{
		{name: "ordinary server", file: File{}},
		{name: "two hours", file: File{Demo: &DemoFile{RealmTTL: "120m"}, Repositories: RepositoryFile{DeletionRetentionDays: &zero}}, wantTTL: 2 * time.Hour},
		{name: "above ceiling", file: File{Demo: &DemoFile{RealmTTL: "121m"}, Repositories: RepositoryFile{DeletionRetentionDays: &zero}}, wantError: "realm_ttl"},
		{name: "below a minute", file: File{Demo: &DemoFile{RealmTTL: "30s"}, Repositories: RepositoryFile{DeletionRetentionDays: &zero}}, wantError: "realm_ttl"},
		{name: "missing ttl", file: File{Demo: &DemoFile{}, Repositories: RepositoryFile{DeletionRetentionDays: &zero}}, wantError: "realm_ttl"},
		{name: "retention implied", file: File{Demo: &DemoFile{RealmTTL: "60m"}}, wantError: "deletion_retention_days"},
		{name: "retention kept", file: File{Demo: &DemoFile{RealmTTL: "60m"}, Repositories: RepositoryFile{DeletionRetentionDays: &seven}}, wantError: "deletion_retention_days"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			policy, err := resolveDemo(c.file)
			if c.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantError) {
					t.Fatalf("err=%v, want %q", err, c.wantError)
				}
				return
			}
			if err != nil || policy.Enabled != (c.file.Demo != nil) || policy.RealmTTL != c.wantTTL {
				t.Fatalf("policy=%+v err=%v", policy, err)
			}
		})
	}

	activated := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	policy := DemoPolicy{Enabled: true, RealmTTL: 2 * time.Hour}
	if policy.Expired(activated, activated.Add(2*time.Hour-time.Nanosecond)) || !policy.Expired(activated, activated.Add(2*time.Hour)) {
		t.Fatal("expiry boundary is not activation plus TTL")
	}
	if (DemoPolicy{}).Expired(activated, activated.Add(1000*time.Hour)) {
		t.Fatal("an ordinary server expired a realm")
	}
}
