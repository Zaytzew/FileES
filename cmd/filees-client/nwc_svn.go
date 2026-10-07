package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"

	"filees/pkg/client"
)

// nwcRemote uses the ordinary desktop activation's svn+ssh tunnel. The
// no-WC policy is local CLI behavior; this identity retains its server grant.
type nwcRemote struct {
	svn, mucc, root string
	env             []string
}

type nwcEntry struct {
	kind string
	size int64
}

func newNWCRemote(cfg Config, root string) (nwcRemote, error) {
	env, err := client.PinnedSSHEnvironment(os.Environ(), cfg.Identity, cfg.KnownHosts, cfg.Port, cfg.Host)
	if err != nil {
		return nwcRemote{}, err
	}
	parsed, err := url.Parse(root)
	if err != nil || (parsed.Scheme != "svn+ssh" && parsed.Scheme != "file") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nwcRemote{}, errors.New("-nwc requires a repository-root svn+ssh URL")
	}
	return nwcRemote{svn: cfg.SVN, mucc: cfg.SVNMucc, root: strings.TrimRight(root, "/"), env: env}, nil
}

func (r nwcRemote) process(ctx context.Context, bin string, args []string, output io.Writer) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = r.env
	cmd.Stdout = output
	var stderr limitedNWCOutput
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w: %s", path.Base(bin), err, stderr.String())
	}
	return nil
}

// XML is decoded as a stream: a long repository history must not become one
// in-memory command output.
func (r nwcRemote) xml(ctx context.Context, args []string, consume func(*xml.Decoder) error) error {
	cmd := exec.CommandContext(ctx, r.svn, append([]string{"--non-interactive", "--no-auth-cache"}, args...)...)
	cmd.Env = r.env
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr limitedNWCOutput
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	decodeErr := consume(xml.NewDecoder(stdout))
	if decodeErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if decodeErr != nil {
		return fmt.Errorf("svn %s XML: %w: %s", args[0], decodeErr, stderr.String())
	}
	if waitErr != nil {
		return fmt.Errorf("svn %s: %w: %s", args[0], waitErr, stderr.String())
	}
	return nil
}

// limitedNWCOutput prevents a failing SVN command from filling process memory.
type limitedNWCOutput struct{ bytes.Buffer }

func (b *limitedNWCOutput) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len() < 4096 {
		_, _ = b.Buffer.Write(p[:min(len(p), 4096-b.Len())])
	}
	return n, nil
}

func (r nwcRemote) revision(ctx context.Context) (int64, error) {
	var rev int64
	found := false
	err := r.xml(ctx, []string{"info", "--xml", r.root + "@"}, func(dec *xml.Decoder) error {
		for {
			tok, err := dec.Token()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return err
			}
			start, ok := tok.(xml.StartElement)
			if !ok || start.Name.Local != "entry" {
				continue
			}
			var entry struct {
				Revision int64  `xml:"revision,attr"`
				URL      string `xml:"url"`
				Root     string `xml:"repository>root"`
			}
			if err := dec.DecodeElement(&entry, &start); err != nil {
				return err
			}
			if strings.TrimRight(entry.URL, "/") != strings.TrimRight(entry.Root, "/") {
				return errors.New("-nwc URL must point to the repository root")
			}
			rev = entry.Revision
			found = true
		}
		if !found || rev < 0 {
			return errors.New("invalid repository revision")
		}
		return nil
	})
	return rev, err
}

func (r nwcRemote) list(ctx context.Context, rev int64) (map[string]nwcEntry, error) {
	out := map[string]nwcEntry{}
	err := r.xml(ctx, []string{"list", "--xml", "-R", "-r", strconv.FormatInt(rev, 10), r.root + "@" + strconv.FormatInt(rev, 10)}, func(dec *xml.Decoder) error {
		for {
			tok, err := dec.Token()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
			start, ok := tok.(xml.StartElement)
			if !ok || start.Name.Local != "entry" {
				continue
			}
			var entry struct {
				Kind string `xml:"kind,attr"`
				Name string `xml:"name"`
				Size int64  `xml:"size"`
			}
			if err := dec.DecodeElement(&entry, &start); err != nil {
				return err
			}
			name := strings.TrimSuffix(entry.Name, "/")
			if !safeNWCRel(name) || (entry.Kind != "file" && entry.Kind != "dir") {
				return fmt.Errorf("unsafe SVN tree entry %q", entry.Name)
			}
			out[name] = nwcEntry{kind: entry.Kind, size: entry.Size}
		}
	})
	return out, err
}

// deletedPaths gathers D events only. An absent file is a restore candidate
// if it or an ancestor was deleted, including a directory deleted as a unit.
func (r nwcRemote) deletedPaths(ctx context.Context, rev int64) (map[string]bool, error) {
	out := map[string]bool{}
	if rev == 0 {
		return out, nil
	}
	err := r.xml(ctx, []string{"log", "--xml", "-v", "-q", "-r", "1:" + strconv.FormatInt(rev, 10), r.root + "@" + strconv.FormatInt(rev, 10)}, func(dec *xml.Decoder) error {
		for {
			tok, err := dec.Token()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
			start, ok := tok.(xml.StartElement)
			if !ok || start.Name.Local != "path" {
				continue
			}
			var action string
			for _, attr := range start.Attr {
				if attr.Name.Local == "action" {
					action = attr.Value
				}
			}
			var changed string
			if err := dec.DecodeElement(&changed, &start); err != nil {
				return err
			}
			changed = strings.TrimPrefix(changed, "/")
			if action == "D" && safeNWCRel(changed) {
				out[changed] = true
			}
		}
	})
	return out, err
}

func (r nwcRemote) objectURL(rel string, rev int64) string {
	parts := strings.Split(rel, "/")
	for i := range parts {
		parts[i] = urlPathEscape(parts[i])
	}
	u := r.root + "/" + strings.Join(parts, "/")
	if rev >= 0 {
		return u + "@" + strconv.FormatInt(rev, 10)
	}
	return u
}

func (r nwcRemote) cat(ctx context.Context, rel string, rev int64, dst io.Writer) error {
	return r.process(ctx, r.svn, []string{"--non-interactive", "--no-auth-cache", "cat", "-r", strconv.FormatInt(rev, 10), r.objectURL(rel, rev)}, dst)
}

func (r nwcRemote) remoteHash(ctx context.Context, rel string, rev int64) (string, error) {
	h := sha256.New()
	if err := r.cat(ctx, rel, rev, h); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (r nwcRemote) commit(ctx context.Context, rev int64, message, actionFile string) error {
	args := []string{"--non-interactive", "--no-auth-cache", "-r", strconv.FormatInt(rev, 10), "-m", message, "-U", r.root, "-X", actionFile}
	return r.process(ctx, r.mucc, args, io.Discard)
}

func safeNWCRel(rel string) bool {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.ContainsAny(rel, "\r\n\\") {
		return false
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}
