package client

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
)

// LocalFileDeleteReconciler acknowledges only one file's WC metadata, offline.
// The caller independently proves the exact remote receipt and completed CFAPI
// deletion, retains the writer fence and guards any recreated physical object.
// No content reads, materialization, filesystem deletion or CLI fallback.
type LocalFileDeleteReconciler interface {
	LocalFileDeleteMetadata(context.Context, string, string, RemoteFileDelete, int64) (string, error)
}

func (c *execClient) LocalFileDeleteMetadata(ctx context.Context, wc, phase string, in RemoteFileDelete, revision int64) (string, error) {
	if err := ValidateRemoteFileDelete(in); err != nil {
		return "", err
	}
	if !filepath.IsAbs(wc) || revision > math.MaxInt32 ||
		(phase != "inspect" && phase != "stage" && phase != "ack" && phase != "verify") ||
		(phase == "inspect" && revision != 0) || (phase != "inspect" && revision <= in.BaseRevision) {
		return "", errors.New("local delete: invalid WC, phase or receipted revision")
	}
	if err := c.nativeRequireFeature(ctx, "local_file_delete_metadata_bound_v1"); err != nil {
		return "", err
	}
	raw, err := c.nativeRun(ctx, wc, "local-file-delete", "--wc", wc, "--phase", phase,
		"--url", in.RootURL, "--uuid", in.UUID, "--base", fmt.Sprint(in.BaseRevision),
		"--revision", fmt.Sprint(revision), in.Source)
	if err != nil {
		return "", err
	}
	return parseLocalFileDeleteResult(raw, phase, in, revision)
}

func parseLocalFileDeleteResult(raw map[string]any, phase string, in RemoteFileDelete, revision int64) (string, error) {
	base, baseErr := nativeRevisionValue(raw, "base_revision", false)
	rev, revErr := nativeRevisionValue(raw, "revision", false)
	state, _ := raw["state"].(string)
	valid := (phase == "inspect" && (state == "present" || state == "virtual")) ||
		(phase == "stage" && state == "scheduled") ||
		((phase == "stage" || phase == "ack" || phase == "verify") && state == "complete")
	_, destination := raw["destination"]
	if !valid || destination || baseErr != nil || revErr != nil ||
		base != in.BaseRevision || rev != revision || raw["mutation"] != "delete-file" ||
		raw["root_url"] != in.RootURL || raw["uuid"] != in.UUID || raw["source"] != in.Source {
		return "", errors.New("local delete: unrelated metadata result; preserve recovery evidence")
	}
	return state, nil
}
