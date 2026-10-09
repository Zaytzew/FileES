package client

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
)

// RemoteFileDeleter is a native, URL-only single-file transaction. The caller
// owns authorization, durable completed deletion intent and local writer fences.
// Neither an absent local file nor a nil inspected receipt authorizes replay.
// Directory deletion is deliberately not part of this contract.
type RemoteFileDeleter interface {
	RemoteFileDeletesEnabled() bool
	RemoteFileDelete(context.Context, RemoteFileDelete) (*RemoteFileDeleteReceipt, error)
	InspectRemoteFileDelete(context.Context, RemoteFileDelete) (*RemoteFileDeleteReceipt, error)
}

type RemoteFileDelete struct {
	RootURL      string `json:"root_url"`
	UUID         string `json:"uuid"`
	BaseRevision int64  `json:"base_revision"`
	OperationID  string `json:"operation_id"`
	Source       string `json:"source"`
	Message      string `json:"message"`
}

type RemoteFileDeleteReceipt struct {
	RemoteFileDelete
	Revision int64 `json:"revision"`
}

func (c *execClient) RemoteFileDeletesEnabled() bool { return filepath.IsAbs(c.nativeSVNPath) }

// ValidateRemoteFileDelete checks binding syntax, not permission to delete.
func ValidateRemoteFileDelete(intent RemoteFileDelete) error {
	if err := validateRemoteMutationBinding(intent.RootURL, intent.UUID, intent.BaseRevision, intent.OperationID, intent.Message); err != nil {
		return err
	}
	if !remoteTreePath(intent.Source) {
		return errors.New("remote delete: canonical non-private relative path required")
	}
	return nil
}

func (c *execClient) RemoteFileDelete(ctx context.Context, intent RemoteFileDelete) (*RemoteFileDeleteReceipt, error) {
	if err := ValidateRemoteFileDelete(intent); err != nil {
		return nil, err
	}
	if err := c.nativeRequireFeature(ctx, "remote_file_delete_bound_v1"); err != nil {
		return nil, err
	}
	raw, err := c.nativeRemote(ctx, "", "remote-file-delete", "--url", intent.RootURL,
		"--uuid", intent.UUID, "--revision", fmt.Sprint(intent.BaseRevision), "--operation-id", intent.OperationID,
		"--message", intent.Message, "--", intent.Source)
	if err != nil {
		return nil, err
	}
	return parseRemoteFileDeleteReceipt(raw, intent)
}

func parseRemoteFileDeleteReceipt(raw map[string]any, intent RemoteFileDelete) (*RemoteFileDeleteReceipt, error) {
	revision, revErr := nativeRevisionValue(raw, "revision", false)
	base, baseErr := nativeRevisionValue(raw, "base_revision", false)
	_, destination := raw["destination"]
	if revErr != nil || baseErr != nil || revision <= intent.BaseRevision || revision > math.MaxInt32 || base != intent.BaseRevision ||
		raw["mutation"] != "delete-file" || destination || raw["uuid"] != intent.UUID || raw["root_url"] != intent.RootURL ||
		raw["operation_id"] != intent.OperationID || raw["source"] != intent.Source || raw["message"] != intent.Message {
		return nil, errors.New("remote delete: unrelated commit receipt; inspect before any retry")
	}
	return &RemoteFileDeleteReceipt{RemoteFileDelete: intent, Revision: revision}, nil
}

// InspectRemoteFileDelete only reads bounded history; nil is not proof of no
// still-running commit. Writer quiescence must be established by the caller.
func (c *execClient) InspectRemoteFileDelete(ctx context.Context, intent RemoteFileDelete) (*RemoteFileDeleteReceipt, error) {
	if err := ValidateRemoteFileDelete(intent); err != nil {
		return nil, err
	}
	if err := c.nativeRequireFeature(ctx, "remote_file_delete_bound_v1"); err != nil {
		return nil, err
	}
	raw, err := c.nativeRemote(ctx, "", "info", "--url", intent.RootURL)
	if err != nil {
		return nil, err
	}
	info, err := parseNativeInfo(raw)
	if err != nil {
		return nil, err
	}
	if info.ReposUUID != intent.UUID || info.ReposRootURL != intent.RootURL || info.URL != intent.RootURL {
		return nil, errors.New("remote delete: repository binding changed during inspection")
	}
	if info.Revision < intent.BaseRevision || info.Revision > math.MaxInt32 {
		return nil, errors.New("remote delete: invalid repository revision during inspection")
	}
	if info.Revision == intent.BaseRevision {
		return nil, nil
	}
	entries, err := c.nativeLog(ctx, intent.RootURL, fmt.Sprintf("%d:%d", info.Revision, intent.BaseRevision+1),
		"--limit", "4097", "--changed-paths", "--revprop", "filees:commit-id")
	if err != nil {
		return nil, err
	}
	return remoteFileDeleteReceiptFromLog(entries, intent, info.Revision)
}

func remoteFileDeleteReceiptFromLog(entries []nativeLogEntry, intent RemoteFileDelete, head int64) (*RemoteFileDeleteReceipt, error) {
	if head < intent.BaseRevision || head > math.MaxInt32 {
		return nil, errors.New("remote delete: invalid repository revision during inspection")
	}
	if len(entries) >= 4097 {
		return nil, errors.New("remote delete: receipt inspection exceeds bounded history")
	}
	var found *RemoteFileDeleteReceipt
	previous := head + 1
	for _, entry := range entries {
		if entry.Revision <= intent.BaseRevision || entry.Revision >= previous {
			return nil, errors.New("remote delete: invalid receipt history order or range")
		}
		previous = entry.Revision
		if entry.Revprops["filees:commit-id"] != intent.OperationID {
			continue
		}
		if found != nil || entry.Message != intent.Message || len(entry.Paths) != 1 {
			return nil, errors.New("remote delete: reused operation identifier or mismatched delete receipt")
		}
		change := entry.Paths[0]
		if change.Path != "/"+intent.Source || change.Action != "D" || change.CopyfromPath != nil || change.CopyfromRev != nil {
			return nil, errors.New("remote delete: mismatched deleted path")
		}
		found = &RemoteFileDeleteReceipt{RemoteFileDelete: intent, Revision: entry.Revision}
	}
	return found, nil
}
