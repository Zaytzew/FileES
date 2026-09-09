package client

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// TransactionCommitter lets the daemon own a durable transaction identifier.
// Lookup errors (including an absent marker) are NOT proof of no effect.
// Callers must retain uncertain intents; CommitWithID is not a retry API.
type TransactionCommitter interface {
	CommitHead(context.Context, string) (int64, error)
	CommitWithID(context.Context, string, string, []string, string, bool, string, int64) (string, int64, error)
	FindCommit(context.Context, string, string, int64) (int64, error)
}

// CommitReconciler repairs only local metadata for an already confirmed
// receipt. It must never publish again or acknowledge a watcher generation.
// Unsupported/ambiguous native states return an error and retain the intent.
type CommitReconciler interface {
	ReconcileCommit(context.Context, string, string, []string, string, int64) error
}

func (c *execClient) ReconcileCommit(ctx context.Context, wc, repoURL string, paths []string, marker string, revision int64) error {
	if _, err := uuid.Parse(marker); err != nil || revision < 1 || strings.TrimSpace(repoURL) == "" {
		return errors.New("invalid confirmed commit identity")
	}
	if !nativeWCOps(c) {
		// This increment does not change the Unix CLI recovery policy.
		return nil
	}
	rels, err := nativeRelatives(wc, paths)
	if err != nil {
		return err
	}
	if len(rels) != len(paths) {
		return errors.New("native recovery refuses WC root targets")
	}
	input, err := nativeCommitTargets(rels)
	if err != nil {
		return err
	}
	if err := c.nativeRequireFeature(ctx, "recover_plain_add_v1"); err != nil {
		return err
	}
	if err := c.nativeRequireFeature(ctx, "writer_lease_v1"); err != nil {
		return err
	}
	r, err := c.nativeRemoteInput(ctx, wc, input, "recover-commit", "--wc", wc, "--url", repoURL,
		"--commit-id", marker, "--revision", strconv.FormatInt(revision, 10), "--targets-stdin")
	if err != nil {
		return err
	}
	got, err := nativeRevisionValue(r, "revision", false)
	if err != nil {
		return err
	}
	if got != revision {
		return errors.New("native recovery receipt revision mismatch")
	}
	return nil
}

func (c *execClient) CommitHead(ctx context.Context, repoURL string) (int64, error) {
	if nativeWCOps(c) {
		if err := c.nativeRequireFeature(ctx, "commit_targets_stdin_v1"); err != nil {
			return 0, err
		}
		if err := c.nativeRequireFeature(ctx, "writer_lease_v1"); err != nil {
			return 0, err
		}
		entries, err := c.nativeLog(ctx, repoURL, "HEAD")
		if err != nil {
			return 0, err
		}
		if len(entries) == 0 {
			return 0, nil
		}
		return entries[0].Revision, nil
	}
	return c.Revision(ctx, repoURL)
}

func (c *execClient) FindCommit(ctx context.Context, repoURL, marker string, firstRevision int64) (int64, error) {
	if _, err := uuid.Parse(marker); err != nil || firstRevision < 1 || strings.TrimSpace(repoURL) == "" {
		return 0, errors.New("invalid durable commit identity")
	}
	return c.commitRevisionByMarker(ctx, repoURL, firstRevision, marker)
}

func (c *execClient) CommitWithID(ctx context.Context, wc, repoURL string, paths []string, message string, keep bool, marker string, firstRevision int64) (string, int64, error) {
	if _, err := uuid.Parse(marker); err != nil || firstRevision < 1 || len(paths) == 0 || strings.TrimSpace(repoURL) == "" {
		return "", 0, errors.New("invalid durable commit request")
	}
	if nativeWCOps(c) {
		return c.nativeCommit(ctx, wc, paths, message, marker, keep)
	}
	args := []string{"commit"}
	if keep {
		args = append(args, "--no-unlock")
	}
	args = append(args, "--with-revprop", "filees:commit-id="+marker, "-m", message)
	args = append(args, c.pathArgs(wc, paths)...)
	out, err := c.run(ctx, wc, args)
	if err != nil {
		return out, 0, err
	}
	rev, err := c.FindCommit(ctx, repoURL, marker, firstRevision)
	return out, rev, err
}
