package whaleworker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	whale "filees/pkg/whale/v1"
)

const generationRevprop = "filees:whale-generation"

type SVNPublisher struct {
	SVNMucc string
	SVNLook string
	Run     func(context.Context, string, ...string) ([]byte, error)
}

func (p SVNPublisher) PublishWhale(ctx context.Context, record *Record, journal Journal, repositoryPath, payloadPath string) (int64, error) {
	if record == nil || record.State != whale.StateCommitting || !filepath.IsAbs(repositoryPath) || !filepath.IsAbs(payloadPath) || !filepath.IsAbs(p.SVNMucc) || !filepath.IsAbs(p.SVNLook) {
		return 0, errors.New("Whale SVN publisher is incomplete")
	}
	storagePath, err := record.Identity.StoragePath()
	if err != nil {
		return 0, err
	}
	if !record.CommitBaseKnown {
		base, err := p.youngest(ctx, repositoryPath)
		if err != nil {
			return 0, err
		}
		record.CommitBaseRevision = base
		record.CommitBaseKnown = true
		if err := journal.Save(*record); err != nil {
			return 0, err
		}
	}
	if revision, err := p.findGeneration(ctx, repositoryPath, record.CommitBaseRevision+1, record.Identity.GenerationID); err != nil {
		return 0, err
	} else if revision != 0 {
		return revision, nil
	}

	existing, err := p.directories(ctx, repositoryPath)
	if err != nil {
		return 0, err
	}
	repositoryURL := fileURL(repositoryPath)
	// Keep command metadata ASCII-only: unattended OpenBSD sessions commonly
	// run in locale C, while the repository URL carries UTF-8 path segments as
	// percent-encoding and the journal retains the original logical path.
	args := []string{"--non-interactive", "--with-revprop", generationRevprop + "=" + record.Identity.GenerationID, "--with-revprop", "filees:whale-sha256=" + record.Identity.SHA256, "-m", "filees: publish Whale " + record.Identity.GenerationID}
	parts := strings.Split(storagePath, "/")
	for index := 1; index < len(parts); index++ {
		dir := strings.Join(parts[:index], "/") + "/"
		if !existing[dir] {
			args = append(args, "mkdir", appendURL(repositoryURL, strings.TrimSuffix(dir, "/")))
		}
	}
	args = append(args, "put", payloadPath, appendURL(repositoryURL, storagePath))
	if _, err := p.run(ctx, p.SVNMucc, args...); err != nil {
		return 0, fmt.Errorf("svnmucc Whale put: %w", err)
	}
	revision, err := p.findGeneration(ctx, repositoryPath, record.CommitBaseRevision+1, record.Identity.GenerationID)
	if err != nil {
		return 0, err
	}
	if revision == 0 {
		return 0, errors.New("Whale commit succeeded without recoverable generation revprop")
	}
	return revision, nil
}

func (p SVNPublisher) youngest(ctx context.Context, repositoryPath string) (int64, error) {
	raw, err := p.run(ctx, p.SVNLook, "youngest", repositoryPath)
	if err != nil {
		return 0, fmt.Errorf("svnlook youngest: %w", err)
	}
	revision, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || revision < 0 {
		return 0, errors.New("svnlook returned invalid youngest revision")
	}
	return revision, nil
}

func (p SVNPublisher) directories(ctx context.Context, repositoryPath string) (map[string]bool, error) {
	raw, err := p.run(ctx, p.SVNLook, "tree", "--full-paths", repositoryPath)
	if err != nil {
		return nil, fmt.Errorf("svnlook tree: %w", err)
	}
	result := make(map[string]bool)
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasSuffix(line, "/") {
			result[strings.TrimPrefix(line, "/")] = true
		}
	}
	return result, nil
}

func (p SVNPublisher) findGeneration(ctx context.Context, repositoryPath string, first int64, generationID string) (int64, error) {
	youngest, err := p.youngest(ctx, repositoryPath)
	if err != nil {
		return 0, err
	}
	for revision := first; revision <= youngest; revision++ {
		properties, err := p.run(ctx, p.SVNLook, "proplist", "--revprop", "-r", strconv.FormatInt(revision, 10), repositoryPath)
		if err != nil {
			return 0, fmt.Errorf("svnlook proplist r%d: %w", revision, err)
		}
		found := false
		for _, name := range strings.Fields(string(properties)) {
			if name == generationRevprop {
				found = true
				break
			}
		}
		if !found {
			continue
		}
		value, err := p.run(ctx, p.SVNLook, "propget", "--revprop", "-r", strconv.FormatInt(revision, 10), repositoryPath, generationRevprop)
		if err != nil {
			return 0, fmt.Errorf("svnlook propget r%d: %w", revision, err)
		}
		if strings.TrimSpace(string(value)) == generationID {
			return revision, nil
		}
	}
	return 0, nil
}

func (p SVNPublisher) run(ctx context.Context, binary string, args ...string) ([]byte, error) {
	if p.Run != nil {
		return p.Run(ctx, binary, args...)
	}
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = append(os.Environ(), "LC_ALL=C.UTF-8")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	raw, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return raw, nil
}

func fileURL(path string) string {
	slashPath := filepath.ToSlash(path)
	// A Windows drive path must be an URL path (/C:/...), otherwise net/url
	// interprets the drive letter as file:// host "c". This also keeps the
	// helper testable on Windows while production uses ordinary OpenBSD paths.
	if len(slashPath) >= 2 && slashPath[1] == ':' {
		slashPath = "/" + slashPath
	}
	return (&url.URL{Scheme: "file", Path: slashPath}).String()
}

func appendURL(root, relative string) string {
	parts := strings.Split(relative, "/")
	for index := range parts {
		parts[index] = url.PathEscape(parts[index])
	}
	return strings.TrimSuffix(root, "/") + "/" + strings.Join(parts, "/")
}
