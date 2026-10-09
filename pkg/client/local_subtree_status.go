package client

import (
	"context"
	"errors"
)

// LocalSubtreeStatus is read-only, explicitly recursive and native-only.
// Status(paths) intentionally uses depth empty and is not a subtree proof.
type LocalSubtreeStatuser interface {
	LocalSubtreeStatus(context.Context, string, string) ([]StatusEntry, error)
}

func (c *execClient) LocalSubtreeStatus(ctx context.Context, wc, rel string) ([]StatusEntry, error) {
	if !nativeWCOps(c) {
		return nil, errors.ErrUnsupported
	}
	selection := rel
	if rel != "." {
		rels, err := nativeRelatives(wc, []string{rel})
		if err != nil || len(rels) != 1 {
			return nil, errors.New("invalid local subtree selection")
		}
		selection = rels[0]
	}
	args := []string{"status", "--inspect-wc", wc, "--depth", "infinity"}
	if selection != "." {
		args = append(args, "--", selection)
	}
	raw, err := c.nativeRun(ctx, wc, args...)
	if err != nil {
		return nil, err
	}
	rows := appendNativeStatus(nil, raw)
	if len(rows) > 4097 {
		return nil, errors.New("local subtree observation exceeds bounded object budget")
	}
	return rows, nil
}
