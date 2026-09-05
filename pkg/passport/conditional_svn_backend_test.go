package passport

import (
	"context"
	"errors"
	"testing"
	"time"

	"filees/pkg/client"
)

type conditionalClient struct {
	client.Client
	calls   int
	force   bool
	failure error
}

func (c *conditionalClient) LockWithComment(_ context.Context, _ string, _ []string, _ string, force bool) (string, error) {
	c.calls++
	c.force = force
	return "", c.failure
}
func (c *conditionalClient) LockInfo(context.Context, string, string) (*client.LockInfo, error) {
	return &client.LockInfo{Token: "new-token"}, nil
}

func TestConditionalBackendHasNoForceFallback(t *testing.T) {
	now := time.Now()
	valid := FormatComment(Metadata{PassportID: "passport", InstanceUID: "instance", PreviousToken: "old-token", IssuedAt: now, ExpiresAt: now.Add(time.Minute), HardExpiresAt: now.Add(time.Hour)})
	denied := errors.New("authority denied")
	for _, tc := range []struct {
		name, comment       string
		configured          bool
		prepareErr, lockErr error
		prepares, locks     int
		success             bool
	}{
		{"fresh lock", valid, false, nil, nil, 0, 1, true},
		{"replacement", valid, true, nil, nil, 1, 1, true},
		{"missing authority", valid, false, nil, nil, 0, 0, false},
		{"invalid metadata", "raw comment", true, nil, nil, 0, 0, false},
		{"missing old token", FormatComment(Metadata{PassportID: "p", InstanceUID: "i", ExpiresAt: now, HardExpiresAt: now}), true, nil, nil, 0, 0, false},
		{"authority failure", valid, true, denied, nil, 1, 0, false},
		{"new lock lost race", valid, true, nil, errors.New("already locked"), 1, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cli := &conditionalClient{failure: tc.lockErr}
			b := ConditionalSVNBackend{SVNBackend: SVNBackend{Client: cli, WC: t.TempDir()}}
			prepared := 0
			if tc.configured {
				b.PrepareReplacement = func(_ context.Context, _ string, metadata Metadata) error {
					prepared++
					if metadata.PreviousToken != "old-token" {
						t.Fatal("previous token changed")
					}
					return tc.prepareErr
				}
			}
			_, _, err := b.Lock(t.Context(), "doc.txt", tc.comment, tc.name != "fresh lock")
			if (err == nil) != tc.success || prepared != tc.prepares || cli.calls != tc.locks || cli.force {
				t.Fatalf("error=%v prepares=%d locks=%d force=%v", err, prepared, cli.calls, cli.force)
			}
		})
	}
}

func TestConditionalBackendDoesNotReleaseWithoutUsableClientContext(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		b := ConditionalSVNBackend{PrepareReplacement: func(context.Context, string, Metadata) error {
			t.Fatal("prepared release without a usable client/context")
			return nil
		}}
		if cancelled {
			b.Client = &conditionalClient{}
			cancel()
		}
		if _, _, err := b.Lock(ctx, "doc.txt", "", true); err == nil {
			t.Fatal("unusable client/context accepted")
		}
		cancel()
	}
}
