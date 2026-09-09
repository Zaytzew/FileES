package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"filees/pkg/client"
)

// Lifecycle, not the transport adapter, grants authority over a working copy.
// The expected identity comes from durable provisioning or an active profile.
// On resume this check precedes cleanup/update; on first checkout it precedes
// every subsequent import mutation. Inspection itself never creates metadata.
type lifecycleSVN struct {
	client.Client
	expected func(repoURL, root string) (workingCopyIdentity, error)
}

func prepareLifecycleWC(ctx context.Context, svn interface {
	GetInfo(context.Context, string) (string, error)
}, root string, expected workingCopyIdentity) error {
	info, err := svn.GetInfo(ctx, root)
	if err != nil {
		return err
	}
	if !infoHasURL(info, expected.RepoURL) {
		return errors.New("working copy URL does not match lifecycle authority")
	}
	return ensureWorkingCopyIdentity(root, expected)
}

func (svn lifecycleSVN) Checkout(ctx context.Context, repoURL, root string) (string, error) {
	expected, err := svn.expected(repoURL, root)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(filepath.Join(root, ".svn")); err == nil {
		if err := prepareLifecycleWC(ctx, svn.Client, root, expected); err != nil {
			return "", err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	out, err := svn.Client.Checkout(ctx, repoURL, root)
	if err != nil {
		return out, err
	}
	return out, prepareLifecycleWC(ctx, svn.Client, root, expected)
}

// Preserve the optional initial-import recovery surface through the wrapper.
func (svn lifecycleSVN) Revert(ctx context.Context, root string, paths []string) (string, error) {
	reverter, ok := svn.Client.(interface {
		Revert(context.Context, string, []string) (string, error)
	})
	if !ok {
		return "", fmt.Errorf("lifecycle client does not support revert")
	}
	return reverter.Revert(ctx, root, paths)
}

// Service projections are not user repositories. A separate identity namespace
// binds this daemon-owned WC to one activation; it grants no server privilege.
func serviceWCPreparation(svn client.Client, serverID, clientID, repoURL string) func(context.Context, string) error {
	return func(ctx context.Context, root string) error {
		if serverID == "" || clientID == "" || repoURL == "" {
			return errors.New("service working-copy authority is incomplete")
		}
		return prepareLifecycleWC(ctx, svn, root, expectedWorkingCopyIdentity(serverID, "service/"+clientID, repoURL))
	}
}
