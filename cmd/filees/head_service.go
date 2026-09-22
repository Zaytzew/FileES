package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

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
	if serverID == "" || strings.ContainsAny(serverID, `/\`) || serverID == "." || serverID == ".." {
		return nil, fmt.Errorf("head: invalid server id")
	}
	dir, err := clientprofile.ServerDir(h.root, serverID)
	if err != nil {
		return nil, err
	}
	profile, err := clientprofile.Load(filepath.Join(dir, "client-profile.json"))
	if err != nil {
		return nil, err
	}
	svn := client.New(client.Options{
		SvnPath: "svn", NativeSVNPath: h.helper(), Timeout: profile.SVNTimeout(),
		LogScope: "svn:head:" + serverID, SSHIdentityFile: profile.IdentityFile,
		SSHKnownHosts: profile.KnownHosts, SSHPort: profile.SSHPort, SSHHostName: profile.Address,
	})
	reader, ok := svn.(headSVN)
	if !ok {
		return nil, fmt.Errorf("head: SVN client cannot list HEAD")
	}
	return reader, nil
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
	dir, err := os.MkdirTemp("", "filees-head-*")
	if err != nil {
		return "", err
	}
	name := path[strings.LastIndex(path, "/")+1:]
	dest := filepath.Join(dir, name)
	if err := svn.CatURL(ctx, headJoin(repoURL, path), dest); err != nil {
		return "", err
	}
	return dest, nil
}
