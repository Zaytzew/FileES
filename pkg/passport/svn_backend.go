package passport

import (
	"context"
	"errors"

	"filees/pkg/client"
)

type SVNBackend struct {
	Client client.Client
	WC     string
}

func (b SVNBackend) Inspect(ctx context.Context, path string) (*Lock, error) {
	if b.Client == nil {
		return nil, errors.New("passport SVN backend: nil client")
	}
	info, err := b.Client.LockInfo(ctx, b.WC, path)
	if err != nil || info == nil {
		return nil, err
	}
	return &Lock{Token: info.Token, Owner: info.Owner, Comment: info.Comment}, nil
}
func (b SVNBackend) Lock(ctx context.Context, path, comment string, force bool) (*Lock, string, error) {
	if b.Client == nil {
		return nil, "", errors.New("passport SVN backend: nil client")
	}
	out, err := b.Client.LockWithComment(ctx, b.WC, []string{path}, comment, force)
	if err != nil {
		return nil, out, err
	}
	info, err := b.Inspect(ctx, path)
	return info, out, err
}
func (b SVNBackend) Unlock(ctx context.Context, path string) (string, error) {
	if b.Client == nil {
		return "", errors.New("passport SVN backend: nil client")
	}
	return b.Client.Unlock(ctx, b.WC, []string{path})
}
