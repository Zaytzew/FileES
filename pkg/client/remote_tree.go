package client

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

// RemoteTreeMover is a URL-only atomic copy+delete, optional and native-only on
// every platform. This is not a WC move and never falls back to a CLI command.
// The coordinator must durably record the intent and serialize/check local work
// BEFORE invoking it. A failed/absent receipt never authorizes blind replay.
type RemoteTreeMover interface {
	RemoteTreeMovesEnabled() bool
	RemoteTreeMove(context.Context, RemoteTreeMove) (*RemoteTreeReceipt, error)
	InspectRemoteTreeMove(context.Context, RemoteTreeMove) (*RemoteTreeReceipt, error)
}

type RemoteTreeMove struct {
	RootURL      string `json:"root_url"`
	UUID         string `json:"uuid"`
	BaseRevision int64  `json:"base_revision"`
	OperationID  string `json:"operation_id"`
	Source       string `json:"source"`
	Destination  string `json:"destination"`
	Message      string `json:"message"`
}

type RemoteTreeReceipt struct {
	RemoteTreeMove
	Revision int64 `json:"revision"`
}

func (c *execClient) RemoteTreeMovesEnabled() bool { return filepath.IsAbs(c.nativeSVNPath) }

// ValidateRemoteTreeMove validates immutable intent before persisting it. It
// does not authenticate the caller or prove the remote/local selection is clean.
func ValidateRemoteTreeMove(move RemoteTreeMove) error { return validateRemoteTreeMove(move) }

func validateRemoteTreeMove(move RemoteTreeMove) error {
	u, err := url.Parse(move.RootURL)
	if err != nil || u == nil || !u.IsAbs() || u.Opaque != "" || !strings.Contains(move.RootURL, "://") ||
		u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(move.RootURL, "\x00\r\n") ||
		move.BaseRevision < 0 || move.BaseRevision > math.MaxInt32 ||
		len(move.Message) == 0 || len(move.Message) > 4096 || !utf8.ValidString(move.Message) || strings.ContainsRune(move.Message, 0) {
		return errors.New("remote tree: invalid repository binding or message")
	}
	for _, id := range []string{move.UUID, move.OperationID} {
		parsed, err := uuid.Parse(id)
		if err != nil || parsed == uuid.Nil || parsed.String() != id {
			return errors.New("remote tree: canonical repository and operation UUID required")
		}
	}
	for _, rel := range []string{move.Source, move.Destination} {
		if !remoteTreePath(rel) {
			return errors.New("remote tree: canonical non-private relative paths required")
		}
	}
	// Also refuse case-only/ancestor collisions before the Windows projection
	// can discover them. Nothing is normalized into a different user selection.
	a, b := strings.ToLower(move.Source), strings.ToLower(move.Destination)
	if a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/") {
		return errors.New("remote tree: source and destination overlap")
	}
	return nil
}

func remoteTreePath(rel string) bool {
	if rel == "" || len(rel) > 4096 || !utf8.ValidString(rel) || path.IsAbs(rel) || path.Clean(rel) != rel ||
		strings.ContainsAny(rel, "\\:\x00\r\n\t") || strings.Count(rel, "/") >= 64 {
		return false
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "." || part == ".." || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") ||
			strings.EqualFold(part, ".svn") || strings.EqualFold(part, ".filees") || strings.EqualFold(part, ".filees-native-probe") {
			return false
		}
		for _, ch := range part {
			if ch < 32 || ch == 127 {
				return false
			}
		}
	}
	return true
}

func (c *execClient) RemoteTreeMove(ctx context.Context, move RemoteTreeMove) (*RemoteTreeReceipt, error) {
	if err := validateRemoteTreeMove(move); err != nil {
		return nil, err
	}
	if err := c.nativeRequireFeature(ctx, "remote_tree_move_bound_v1"); err != nil {
		return nil, err
	}
	raw, err := c.nativeRemote(ctx, "", "remote-tree-move", "--url", move.RootURL,
		"--uuid", move.UUID, "--revision", fmt.Sprint(move.BaseRevision), "--operation-id", move.OperationID,
		"--message", move.Message, "--", move.Source, move.Destination)
	if err != nil {
		return nil, err
	}
	return parseRemoteTreeReceipt(raw, move)
}

func parseRemoteTreeReceipt(raw map[string]any, move RemoteTreeMove) (*RemoteTreeReceipt, error) {
	revision, err := nativeRevisionValue(raw, "revision", false)
	base, baseErr := nativeRevisionValue(raw, "base_revision", false)
	if err != nil || baseErr != nil || revision <= move.BaseRevision || base != move.BaseRevision ||
		raw["uuid"] != move.UUID || raw["root_url"] != move.RootURL || raw["operation_id"] != move.OperationID ||
		raw["source"] != move.Source || raw["destination"] != move.Destination {
		return nil, errors.New("remote tree: invalid or unrelated commit receipt; inspect before any retry")
	}
	return &RemoteTreeReceipt{RemoteTreeMove: move, Revision: revision}, nil
}

// InspectRemoteTreeMove is read-only. Nil means no matching receipt in the
// bounded, successfully read history, NOT proof a still-running writer cannot
// commit later. The coordinator must establish writer quiescence separately.
// A truncated/unreadable history or a reused marker is never treated as success.
func (c *execClient) InspectRemoteTreeMove(ctx context.Context, move RemoteTreeMove) (*RemoteTreeReceipt, error) {
	if err := validateRemoteTreeMove(move); err != nil {
		return nil, err
	}
	if err := c.nativeRequireFeature(ctx, "remote_tree_move_bound_v1"); err != nil {
		return nil, err
	}
	raw, err := c.nativeRemote(ctx, "", "info", "--url", move.RootURL)
	if err != nil {
		return nil, err
	}
	info, err := parseNativeInfo(raw)
	if err != nil {
		return nil, err
	}
	if info.ReposUUID != move.UUID || info.ReposRootURL != move.RootURL || info.URL != move.RootURL {
		return nil, errors.New("remote tree: repository binding changed during inspection")
	}
	if info.Revision <= move.BaseRevision {
		return nil, nil
	}
	entries, err := c.nativeLog(ctx, move.RootURL, fmt.Sprintf("%d:%d", info.Revision, move.BaseRevision+1),
		"--limit", "4097", "--changed-paths", "--revprop", "filees:commit-id")
	if err != nil {
		return nil, err
	}
	return remoteTreeReceiptFromLog(entries, move, info.Revision)
}

func remoteTreeReceiptFromLog(entries []nativeLogEntry, move RemoteTreeMove, head int64) (*RemoteTreeReceipt, error) {
	if len(entries) >= 4097 {
		return nil, errors.New("remote tree: receipt inspection exceeds bounded history")
	}
	var found *RemoteTreeReceipt
	previous := head + 1
	for _, entry := range entries {
		if entry.Revision <= move.BaseRevision || entry.Revision >= previous {
			return nil, errors.New("remote tree: invalid receipt history order or range")
		}
		previous = entry.Revision
		if entry.Revprops["filees:commit-id"] != move.OperationID {
			continue
		}
		deleted, copied := false, false
		for _, change := range entry.Paths {
			if change.Path == "/"+move.Source && change.Action == "D" && change.CopyfromPath == nil && change.CopyfromRev == nil {
				deleted = true
			}
			if change.Path == "/"+move.Destination && change.Action == "A" && change.CopyfromPath != nil &&
				*change.CopyfromPath == "/"+move.Source && change.CopyfromRev != nil && *change.CopyfromRev == move.BaseRevision {
				copied = true
			}
		}
		if found != nil || len(entry.Paths) != 2 || !deleted || !copied || entry.Message != move.Message {
			return nil, errors.New("remote tree: reused operation identifier or mismatched copy/delete receipt")
		}
		found = &RemoteTreeReceipt{RemoteTreeMove: move, Revision: entry.Revision}
	}
	return found, nil
}
