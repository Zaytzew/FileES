package client

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
)

// Private WC metadata, not a remote lock observation or permission to delete.
// The checksum is the bound pristine SHA1; reading it never opens file bytes.
type LocalFileDeleteProof struct {
	BaseRevision int64
	ChecksumSHA1 string
	LockToken    string `json:"-"`
}

type LocalFileDeletePreflighter interface {
	LocalFileDeletePreflight(context.Context, string, RemoteFileDelete) (LocalFileDeleteProof, error)
}

func (c *execClient) LocalFileDeletePreflight(ctx context.Context, wc string, in RemoteFileDelete) (LocalFileDeleteProof, error) {
	if err := ValidateRemoteFileDelete(in); err != nil {
		return LocalFileDeleteProof{}, err
	}
	if !filepath.IsAbs(wc) {
		return LocalFileDeleteProof{}, errors.New("local delete preflight requires absolute WC")
	}
	if err := c.nativeRequireFeature(ctx, "local_file_delete_preflight_v1"); err != nil {
		return LocalFileDeleteProof{}, err
	}
	raw, err := c.nativeRun(ctx, wc, "local-file-delete", "--wc", wc, "--phase", "preflight", "--url", in.RootURL, "--uuid", in.UUID, "--base", fmt.Sprint(in.BaseRevision), "--revision", "0", in.Source)
	if err != nil {
		return LocalFileDeleteProof{}, err
	}
	return parseLocalFileDeleteProof(raw, in)
}

func parseLocalFileDeleteProof(raw map[string]any, in RemoteFileDelete) (LocalFileDeleteProof, error) {
	base, e1 := nativeRevisionValue(raw, "local_base_revision", false)
	bound, e2 := nativeRevisionValue(raw, "base_revision", false)
	rev, e3 := nativeRevisionValue(raw, "revision", false)
	checksum, checksumOK := raw["checksum_sha1"].(string)
	token, tokenOK := raw["local_lock_token"].(string)
	_, checksumPresent := raw["checksum_sha1"]
	_, lockPresent := raw["local_lock_token"]
	_, destination := raw["destination"]
	state := raw["state"]
	valid := state == "virtual" && raw["checksum_sha1"] == nil && raw["local_lock_token"] == nil
	if state == "present" {
		decoded, e := hex.DecodeString(checksum)
		valid = checksumOK && e == nil && len(decoded) == 20 && strings.ToLower(checksum) == checksum && (raw["local_lock_token"] == nil || tokenOK && token != "" && len(token) <= 4095 && !strings.ContainsAny(token, "\x00\r\n"))
	}
	if !valid || !checksumPresent || !lockPresent || destination || e1 != nil || e2 != nil || e3 != nil || base < 1 || base > in.BaseRevision || bound != in.BaseRevision || rev != 0 || raw["mutation"] != "delete-file" || raw["root_url"] != in.RootURL || raw["uuid"] != in.UUID || raw["source"] != in.Source {
		return LocalFileDeleteProof{}, errors.New("local delete preflight: unrelated or incomplete proof")
	}
	return LocalFileDeleteProof{BaseRevision: base, ChecksumSHA1: checksum, LockToken: token}, nil
}

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
