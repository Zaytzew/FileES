package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"filees/pkg/clientprofile"
	"filees/pkg/whaleclient"
)

type whaleFlags struct {
	profile, state, id, repo, path, source, dest string
	revision                                     int64
	timeout                                      time.Duration
}

func cmdWhale(action string, args []string) int {
	flags := flag.NewFlagSet(action, flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	var opt whaleFlags
	flags.StringVar(&opt.profile, "profile", "", "absolute activated client-profile.json")
	flags.StringVar(&opt.state, "state", "", "absolute private Whale operation directory")
	flags.StringVar(&opt.id, "id", "", "Whale operation ID for confirm/status/retry/resume")
	flags.StringVar(&opt.repo, "repo", "", "logical repository UUID")
	flags.StringVar(&opt.path, "path", "", "logical path inside repository")
	flags.StringVar(&opt.source, "source", "", "absolute source file for put")
	flags.StringVar(&opt.dest, "dest", "", "absolute new destination for get")
	flags.Int64Var(&opt.revision, "revision", 0, "snapshot revision for get quote")
	flags.DurationVar(&opt.timeout, "timeout", 24*time.Hour, "maximum one-shot transfer time")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return 2
	}
	if !filepath.IsAbs(opt.profile) || !filepath.IsAbs(opt.state) || opt.timeout <= 0 {
		fmt.Fprintln(os.Stderr, action+": absolute --profile, --state and positive --timeout are required")
		return 2
	}
	profile, err := clientprofile.Load(opt.profile)
	if err != nil {
		fmt.Fprintln(os.Stderr, action+":", err)
		return 1
	}
	if err := os.MkdirAll(opt.state, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, action+":", err)
		return 1
	}
	if err := lockWhaleSandbox(opt, profile); err != nil {
		fmt.Fprintln(os.Stderr, action+" sandbox:", err)
		return 1
	}
	manager, err := whaleclient.NewManager(opt.state, []clientprofile.Profile{profile})
	if err != nil {
		fmt.Fprintln(os.Stderr, action+":", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), opt.timeout)
	defer cancel()
	var op whaleclient.Operation
	switch action {
	case "whale-put":
		if !filepath.IsAbs(opt.source) {
			return whaleUsageError(action, "--source must be absolute")
		}
		op, err = manager.BeginPut(ctx, profile.ServerID, opt.repo, opt.path, opt.source)
	case "whale-get-quote":
		if !filepath.IsAbs(opt.dest) {
			return whaleUsageError(action, "--dest must be absolute")
		}
		op, err = manager.BeginGetTarget(ctx, profile.ServerID, opt.repo, opt.path, opt.revision, opt.dest)
	case "whale-get-confirm":
		op, err = manager.ConfirmGet(ctx, opt.id)
	case "whale-retry":
		op, err = manager.Retry(ctx, opt.id)
	case "whale-resume":
		if opt.id == "" {
			return whaleUsageError(action, "--id is required")
		}
		if err = manager.Resume(ctx); err == nil {
			op, err = manager.Get(opt.id)
		}
	case "whale-status":
		op, err = manager.Get(opt.id)
		if err == nil {
			printWhaleOperation(op)
			return 0
		}
	default:
		return whaleUsageError(action, "unknown Whale action")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, action+":", err)
		return 1
	}
	fmt.Fprintf(os.Stdout, "operation_id=%s\n", op.OperationID)
	if err := waitWhale(ctx, manager, op.OperationID); err != nil {
		fmt.Fprintln(os.Stderr, action+":", err)
		return 1
	}
	return 0
}

func whaleUsageError(action, message string) int {
	fmt.Fprintln(os.Stderr, action+":", message)
	return 2
}

func waitWhale(ctx context.Context, manager *whaleclient.Manager, id string) error {
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	for {
		op, err := manager.Get(id)
		if err != nil {
			return err
		}
		switch op.State {
		case whaleclient.StatePublished, whaleclient.StateLocal, whaleclient.StateAwaitingConfirmation:
			printWhaleOperation(op)
			return nil
		case whaleclient.StatePaused, whaleclient.StateFailed, whaleclient.StateCancelled:
			printWhaleOperation(op)
			return fmt.Errorf("Whale operation stopped in state %s; inspect status and retry deliberately", op.State)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

func printWhaleOperation(op whaleclient.Operation) {
	// Print only operational metadata. The durable operation may contain
	// confirmation tokens and local paths; those never enter stdout or logs.
	fmt.Fprintf(os.Stdout, "operation_id=%s state=%s bytes=%d revision=%d\n",
		op.OperationID, op.State, op.BytesHave, op.PublishedRevision)
	if op.State == whaleclient.StateAwaitingConfirmation && op.Identity != nil {
		fmt.Fprintf(os.Stdout, "quote_bytes=%d; run whale-get-confirm --id %s after review\n",
			op.Identity.ExpectedSize, op.OperationID)
	}
}
