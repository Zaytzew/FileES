package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"filees/pkg/config"
	"filees/pkg/errmap"
	"filees/pkg/talk"
)

type interruptedPolicyClient struct {
	policyMigrationClient
	fault  error
	cancel context.CancelFunc
}

func (c *interruptedPolicyClient) PropList(context.Context, string, string) (map[string]bool, error) {
	if c.cancel != nil {
		c.cancel()
	}
	return nil, c.fault
}

func TestEditingPolicyReportsOnlyIndependentFailuresDuringStop(t *testing.T) {
	for _, locked := range []bool{false, true} {
		for _, tt := range []struct {
			name   string
			stop   bool
			fault  error
			report bool
		}{
			{"own cancellation", true, fmt.Errorf("native SVN propget failed: %w", errors.Join(context.Canceled, context.Canceled)), false},
			{"independent cancellation", false, context.Canceled, true},
			{"real failure during stop", true, errors.New("property metadata unreadable"), true},
			{"independent deadline", false, context.DeadlineExceeded, true},
		} {
			t.Run(fmt.Sprintf("locked=%t/%s", locked, tt.name), func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				cli := &interruptedPolicyClient{fault: tt.fault}
				if tt.stop {
					cli.cancel = cancel
				}
				stateDir := policyStateDir(t)
				var log bytes.Buffer
				applyEditingPolicyMigration(ctx, config.Repo{ID: "fixture"}, cli, filepath.Dir(stateDir), stateDir, "instance", locked, errmap.NewSink(&log, "fixture"), talk.With("policy-cancel-test"))
				if (log.Len() != 0) != tt.report {
					t.Fatalf("report=%t want=%t log=%s", log.Len() != 0, tt.report, log.String())
				}
				if cli.commits != 0 || len(cli.sets) != 0 || len(cli.dels) != 0 || readAppliedEditingPolicy(stateDir) != "" {
					t.Fatal("interrupted inspection mutated metadata or claimed completion")
				}
			})
		}
	}
}
