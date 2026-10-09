package client

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
)

// LocalTreeReconciler changes only WC metadata. The caller retains the physical
// object guards and durable move fence and proves the remote receipt separately.
// Prepare may fetch absent directory metadata at BASE, never file contents or
// deepen an existing directory. The remaining phases are offline; no CLI or
// recursive ACK fallback is allowed.
type LocalTreeReconciler interface {
	LocalTreeMetadata(context.Context, string, string, RemoteTreeMove, int64) (string, error)
}

func (c *execClient) LocalTreeMetadata(ctx context.Context, wc, phase string, move RemoteTreeMove, revision int64) (string, error) {
	if err := validateRemoteTreeMove(move); err != nil {
		return "", err
	}
	if !filepath.IsAbs(wc) || move.BaseRevision < 1 || revision > math.MaxInt32 ||
		(phase != "prepare" && phase != "inspect" && phase != "stage" && phase != "ack" && phase != "verify") ||
		((phase == "inspect" || phase == "prepare") && revision != 0) || (phase != "inspect" && phase != "prepare" && revision <= move.BaseRevision) {
		return "", errors.New("local tree: invalid WC, phase or receipted revision")
	}
	if err := c.nativeRequireFeature(ctx, "local_tree_metadata_bound_v1"); err != nil {
		return "", err
	}
	raw, err := c.nativeRun(ctx, wc, "local-tree", "--wc", wc, "--phase", phase,
		"--url", move.RootURL, "--uuid", move.UUID, "--base", fmt.Sprint(move.BaseRevision),
		"--revision", fmt.Sprint(revision), move.Source, move.Destination)
	if err != nil {
		return "", err
	}
	return parseLocalTreeResult(raw, phase, move, revision)
}

func parseLocalTreeResult(raw map[string]any, phase string, move RemoteTreeMove, revision int64) (string, error) {
	base, baseErr := nativeRevisionValue(raw, "base_revision", false)
	rev, revErr := nativeRevisionValue(raw, "revision", false)
	state, _ := raw["state"].(string)
	validState := state == phase || (phase == "stage" && state == "scheduled") ||
		(phase != "inspect" && phase != "prepare" && state == "complete")
	if !validState || baseErr != nil || revErr != nil || base != move.BaseRevision || rev != revision ||
		raw["root_url"] != move.RootURL || raw["uuid"] != move.UUID ||
		raw["source"] != move.Source || raw["destination"] != move.Destination {
		return "", errors.New("local tree: unrelated metadata result; preserve recovery evidence")
	}
	return state, nil
}
