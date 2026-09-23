package main

import (
	"context"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filees/pkg/client"
	"filees/pkg/clientprofile"
	contract "filees/pkg/contract/v1"
)

// headService reads HEAD for the unattached browser: one directory listing or
// one file, over the activated profile's identity, with no working copy.
// Choosing paths is not here - see ipcserver.HeadService.
type headService struct {
	root   string
	helper func() string
}

func (h headService) svn(serverID string) (headSVN, error) {
	svn, err := profileSVN(h.root, h.helper(), serverID, "svn:head:")
	if err != nil {
		return nil, err
	}
	reader, ok := svn.(headSVN)
	if !ok {
		return nil, fmt.Errorf("head: SVN client cannot list HEAD")
	}
	return reader, nil
}

// profileSVN is an SVN client with the identity of the activated profile for
// serverID: reads of HEAD without a working copy, for the HEAD browser and the
// Explorer anchor alike.
func profileSVN(root, nativeHelper, serverID, scope string) (client.Client, error) {
	if serverID == "" || strings.ContainsAny(serverID, `/\`) || serverID == "." || serverID == ".." {
		return nil, fmt.Errorf("head: invalid server id")
	}
	dir, err := clientprofile.ServerDir(root, serverID)
	if err != nil {
		return nil, err
	}
	profile, err := clientprofile.Load(filepath.Join(dir, "client-profile.json"))
	if err != nil {
		return nil, err
	}
	return client.New(client.Options{
		SvnPath: "svn", NativeSVNPath: nativeHelper, Timeout: profile.SVNTimeout(),
		LogScope: scope + serverID, SSHIdentityFile: profile.IdentityFile,
		SSHKnownHosts: profile.KnownHosts, SSHPort: profile.SSHPort, SSHHostName: profile.Address,
	}), nil
}

type headSVN interface {
	ListImmediate(context.Context, string) ([]client.RemoteEntry, error)
	CatURL(context.Context, string, string) error
}

func headJoin(root, rel string) string {
	base := strings.TrimSuffix(root, "/")
	if rel == "" {
		return base
	}
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return base + "/" + strings.Join(parts, "/")
}

func (h headService) HeadList(ctx context.Context, serverID, repoURL, path string) ([]contract.RepoHeadEntry, error) {
	svn, err := h.svn(serverID)
	if err != nil {
		return nil, err
	}
	entries, err := svn.ListImmediate(ctx, headJoin(repoURL, path))
	if err != nil {
		return nil, err
	}
	out := make([]contract.RepoHeadEntry, 0, len(entries))
	for _, entry := range entries {
		item := contract.RepoHeadEntry{Name: entry.Name, Kind: entry.Kind, Revision: entry.Revision}
		if entry.Kind == "file" && entry.Size >= 0 {
			item.Size = entry.Size
		}
		out = append(out, item)
	}
	return out, nil
}

func (h headService) HeadCat(ctx context.Context, serverID, repoURL, path string) (string, error) {
	svn, err := h.svn(serverID)
	if err != nil {
		return "", err
	}
	// Yesterday's previews are nobody's: the copy is read-only, goes nowhere
	// and the person who looked at it has long closed the application. Left
	// alone they accumulate in the system temp folder for as long as the
	// client is installed.
	prunePreviews(os.TempDir(), previewLifetime, time.Now())
	dir, err := os.MkdirTemp("", previewPrefix+"*")
	if err != nil {
		return "", err
	}
	name := path[strings.LastIndex(path, "/")+1:]
	dest := filepath.Join(dir, name)
	if err := svn.CatURL(ctx, headJoin(repoURL, path), dest); err != nil {
		return "", err
	}
	// A preview is a copy that goes nowhere: nothing written to it reaches the
	// server. Marking it read-only (the read-only attribute on Windows) makes
	// Word, Excel and most editors open it as read-only instead of letting
	// someone edit a file whose changes would silently stay in a temp folder.
	if err := markPreviewReadOnly(dest); err != nil {
		return "", err
	}
	return dest, nil
}

func markPreviewReadOnly(path string) error {
	return os.Chmod(path, 0o444)
}

const (
	previewPrefix = "filees-head-"
	// previewLifetime outlives any reasonable look at a file while making
	// sure a folder does not survive the session that made it. An editor
	// still holding the copy simply keeps it: a refused removal is not an
	// error here.
	previewLifetime = 12 * time.Hour
)

// prunePreviews removes preview folders this client made earlier than
// lifetime ago. Preview copies are read-only, which on Windows stops
// os.RemoveAll, so the files are made writable first.
func prunePreviews(root string, lifetime time.Duration, now time.Time) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), previewPrefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil || now.Sub(info.ModTime()) < lifetime {
			continue
		}
		path := filepath.Join(root, entry.Name())
		_ = filepath.WalkDir(path, func(name string, item fs.DirEntry, err error) error {
			if err == nil && !item.IsDir() {
				_ = os.Chmod(name, 0o666)
			}
			return nil
		})
		_ = os.RemoveAll(path)
	}
}
