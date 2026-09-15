package main

import (
	"context"
	"errors"
	"net/url"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"

	"filees/pkg/client"
	"filees/pkg/clientprofile"
	contract "filees/pkg/contract/v1"
	"filees/pkg/historyexport"
	"filees/pkg/ipcserver"
	"filees/pkg/shout"
)

// defaultHistoryExportPath holds one journal record per export, so a restart
// can finish a half-moved result or remove an abandoned staging folder.
func defaultHistoryExportPath() string {
	return filepath.Join(filepath.Dir(clientprofile.DefaultRoot()), "history-exports")
}

// historyExportFoldsCase: on Windows a.txt and A.txt are one file, so the
// planner brackets the second instead of letting the helper refuse it.
func historyExportFoldsCase() bool { return goruntime.GOOS == "windows" }

func (h historyService) exportReader(serverID string) (historyexport.Reader, error) {
	reader, err := h.reader(serverID)
	if err != nil {
		return nil, err
	}
	tree, ok := reader.(historyexport.Reader)
	if !ok {
		return nil, errors.New("history: SVN client cannot export trees")
	}
	return tree, nil
}

// historyService answers Wehikuł czasu reads through a client built from the
// server's profile. The IPC handlers own the owner gate and paging; this only
// talks to the repository. Nothing is cached between calls: a client is cheap,
// and a profile rewritten by reactivation must count on the next read.
type historyService struct {
	root   string
	helper func() string
}

func (h historyService) HistoryEnabled() bool { return filepath.IsAbs(h.helper()) }

func (h historyService) reader(serverID string) (client.HistoryReader, error) {
	if serverID == "" || serverID == "." || serverID == ".." || strings.ContainsAny(serverID, `/\`) {
		return nil, errors.New("history: invalid server id")
	}
	profile, err := clientprofile.Load(filepath.Join(h.root, serverID, "client-profile.json"))
	if err != nil {
		return nil, err
	}
	svn := client.New(client.Options{SvnPath: "svn", NativeSVNPath: h.helper(), Timeout: profile.SVNTimeout(), LogScope: "svn:history:" + serverID, SSHIdentityFile: profile.IdentityFile, SSHKnownHosts: profile.KnownHosts, SSHPort: profile.SSHPort, SSHHostName: profile.Address})
	reader, ok := svn.(client.HistoryReader)
	if !ok || !reader.HistoryEnabled() {
		return nil, errors.New("history: native SVN helper is not configured")
	}
	return reader, nil
}

func (h historyService) HistoryRepositoryUUID(ctx context.Context, serverID, repoURL string) (string, error) {
	reader, err := h.reader(serverID)
	if err != nil {
		return "", err
	}
	return reader.HistoryRepositoryUUID(ctx, repoURL)
}

func (h historyService) HistoryRevisionAt(ctx context.Context, serverID, repoURL string, moment time.Time) (int64, string, error) {
	reader, err := h.reader(serverID)
	if err != nil {
		return 0, "", err
	}
	return reader.HistoryRevisionAt(ctx, repoURL, moment)
}

func (h historyService) HistoryLog(ctx context.Context, serverID, repoURL string, newest, oldest int64, limit int) ([]ipcserver.HistoryLogEntry, error) {
	reader, err := h.reader(serverID)
	if err != nil {
		return nil, err
	}
	commits, err := reader.HistoryLog(ctx, repoURL, newest, oldest, limit)
	if err != nil {
		return nil, err
	}
	return historyLogEntries(commits), nil
}

func historyLogEntries(commits []client.HistoryCommit) []ipcserver.HistoryLogEntry {
	out := make([]ipcserver.HistoryLogEntry, 0, len(commits))
	for _, commit := range commits {
		entry := ipcserver.HistoryLogEntry{Revision: commit.Revision, Date: commit.Date, Author: commit.Author}
		// Only a shout is shown. Other messages are the daemon's own
		// bookkeeping and mean nothing to the person browsing.
		if comment, ok := shout.Parse(commit.Message); ok {
			entry.Shout = comment
		}
		for _, change := range commit.Changes {
			item := contract.RepoHistoryChange{Path: change.Path, Action: change.Action, Kind: change.Kind}
			if change.CopyFromRevision >= 0 {
				item.CopyFromPath, item.CopyFromRevision = change.CopyFromPath, change.CopyFromRevision
			}
			entry.Changes = append(entry.Changes, item)
		}
		out = append(out, entry)
	}
	return out
}

func (h historyService) HistoryList(ctx context.Context, serverID, repoURL, path string, revision int64) ([]contract.RepoHistoryEntry, error) {
	reader, err := h.reader(serverID)
	if err != nil {
		return nil, err
	}
	entries, err := reader.HistoryList(ctx, historyURL(repoURL, path), revision)
	if client.HistoryPathAbsent(err) {
		return nil, ipcserver.ErrHistoryPathAbsent
	}
	if err != nil {
		return nil, err
	}
	out := make([]contract.RepoHistoryEntry, 0, len(entries))
	for _, entry := range entries {
		item := contract.RepoHistoryEntry{Name: entry.Name, Kind: entry.Kind, LastChangedRevision: entry.LastChangedRevision, LastChangedDate: entry.LastChangedDate, LastAuthor: entry.LastAuthor}
		if entry.Kind == "file" && entry.Size >= 0 {
			size := entry.Size
			item.Size = &size
		}
		out = append(out, item)
	}
	return out, nil
}

// historyURL escapes each segment of an already validated relative path.
func historyURL(root, path string) string {
	base := strings.TrimSuffix(root, "/")
	if path == "" {
		return base
	}
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return base + "/" + strings.Join(segments, "/")
}
