package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"filees/internal/durable"
	"filees/pkg/clientprofile"
	"filees/pkg/clientview"
	"filees/pkg/deploy"
	"filees/pkg/privatefile"

	"github.com/google/uuid"
)

const publishJobSchema = "filees.cli-publish/v1"

// Knowledge of a selected source/repository, never a substitute for a live grant.
type publishJob struct {
	Schema            string `json:"schema"`
	Name              string `json:"name"`
	Profile           string `json:"profile,omitempty"`
	Activation        string `json:"activation,omitempty"`
	ServerID          string `json:"server_id"`
	ClientID          string `json:"client_id"`
	RealmID           string `json:"realm_id"`
	RepoID            string `json:"repo_id"`
	RepoURL           string `json:"repo_url"`
	Source            string `json:"source"`
	SVN               string `json:"svn"`
	SVNMucc           string `json:"svnmucc"`
	SSH               string `json:"ssh"`
	MinimumGeneration int64  `json:"minimum_generation"`
}

func defaultPublishRoot() string {
	root := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(root) {
		home, _ := os.UserHomeDir()
		root = filepath.Join(home, ".config")
	}
	return filepath.Join(root, "filees-cli")
}

func cmdAutoconfig(args []string) int {
	f := flag.NewFlagSet("autoconfig", flag.ContinueOnError)
	profilePath := f.String("profile", "", "absolute existing client-profile.json")
	activation := f.String("activation", "", "absolute activated server directory of older CLI")
	stateRoot := f.String("state-root", clientprofile.DefaultRoot(), "existing activated servers directory")
	server := f.String("server", "", "server name or ID when several are activated")
	repoName := f.String("repo", "", "exact repository display name; omit to list")
	source := f.String("source", "", "absolute exact source directory")
	name := f.String("name", "", "short name for subsequent publish")
	root := f.String("config-root", defaultPublishRoot(), "absolute private CLI profiles directory")
	svn := f.String("svn", "", "svn executable; default: discover on PATH")
	mucc := f.String("svnmucc", "", "svnmucc executable; default: discover on PATH")
	ssh := f.String("ssh", "", "ssh executable; default: discover on PATH")
	timeout := f.Duration("timeout", 2*time.Minute, "configuration timeout")
	if err := f.Parse(args); err != nil || f.NArg() != 0 || *timeout <= 0 {
		return 2
	}
	p, pp, ap, err := selectPublishActivation(*profilePath, *activation, *stateRoot, *server)
	if err != nil {
		return publishError("autoconfig", err)
	}
	job := publishJob{Schema: publishJobSchema, Name: *name, Profile: pp, Activation: ap, ServerID: p.ServerID, ClientID: p.ClientID}
	for _, tool := range []struct {
		name, requested string
		dest            *string
	}{{"svn", *svn, &job.SVN}, {"svnmucc", *mucc, &job.SVNMucc}, {"ssh", *ssh, &job.SSH}} {
		program := tool.requested
		if program == "" {
			program = tool.name
		}
		resolved, e := exec.LookPath(program)
		if e != nil {
			return publishError("autoconfig tools", e)
		}
		*tool.dest, err = filepath.Abs(resolved)
		if err != nil {
			return publishError("autoconfig tools", err)
		}
	}
	// Listing needs no saved job/source. A private temporary state holds SVN's
	// configuration and is unveiled like the ordinary no-WC publisher.
	listing := *repoName == ""
	var dir string
	if listing {
		dir, err = os.MkdirTemp("", "filees-cli-repositories-")
		if err == nil {
			defer os.RemoveAll(dir)
			job.Name = "repositories"
			job.Source = dir
		}
	} else {
		if !safeCopyID(job.Name) || len(job.Name) > 80 {
			return publishError("autoconfig", errors.New("--name must be 1-80 ASCII letters, digits, hyphens or underscores"))
		}
		if !filepath.IsAbs(*source) || strings.ContainsAny(*source, "\x00\r\n") {
			return publishError("autoconfig", errors.New("--source must be absolute and explicit"))
		}
		job.Source, err = filepath.EvalSymlinks(*source)
		if err == nil {
			dir, err = preparePublishDirectory(*root, job.Name, job.Source)
		}
		if err == nil {
			err = checkNWCSource(job.Source, dir)
		}
	}
	if err != nil {
		return publishError("autoconfig", err)
	}
	lock, err := acquireFileLock(filepath.Join(dir, "config.lock"))
	if err != nil {
		return publishError("autoconfig", err)
	}
	defer lock.Close()
	cfg, err := job.config(p, dir)
	if err != nil {
		return publishError("autoconfig", err)
	}
	if err = lockPublishSandbox(cfg); err != nil {
		return publishError("autoconfig sandbox", err)
	}
	if err = prepareSSHPath(cfg); err != nil {
		return publishError("autoconfig tools", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	view, err := fetchPublishView(ctx, cfg, p)
	if err != nil {
		return publishError("autoconfig projection", err)
	}
	if listing {
		fmt.Fprintf(os.Stdout, "server=%s\n", view.ServerDisplayName)
		for _, r := range view.Repositories {
			fmt.Fprintf(os.Stdout, "%s\t%s\t%s\n", r.DisplayName, r.Access, r.State)
		}
		return 0
	}
	repo, err := selectPublishRepository(view, *repoName, "", p)
	if err != nil {
		return publishError("autoconfig repository", err)
	}
	job.RealmID, job.RepoID, job.RepoURL, job.MinimumGeneration = view.RealmID, repo.RepoID, repo.URL, view.Generation
	if err := storePublishJob(dir, job, view); err != nil {
		return publishError("autoconfig state", err)
	}
	fmt.Fprintf(os.Stdout, "configured %s\nserver=%s\nrepository=%s\nsource=%s\nstate=%s\n", job.Name, view.ServerDisplayName, repo.DisplayName, job.Source, dir)
	fmt.Fprintf(os.Stdout, "publish with: filees-client publish %s --config-root '%s'\n", job.Name, strings.ReplaceAll(filepath.Dir(dir), "'", "'\\''"))
	return 0
}

func cmdPublish(args []string) int {
	// Accept the short name before flags so cron can use exactly publish NAME.
	if len(args) == 0 || !safeCopyID(args[0]) || len(args[0]) > 80 {
		return publishError("publish", errors.New("usage: publish NAME [--config-root DIRECTORY] [--message TEXT] [--timeout DURATION]"))
	}
	name := args[0]
	f := flag.NewFlagSet("publish", flag.ContinueOnError)
	root := f.String("config-root", defaultPublishRoot(), "absolute private CLI profiles directory")
	message := f.String("message", "FileES publish", "commit message")
	timeout := f.Duration("timeout", 30*time.Minute, "whole publication timeout")
	if err := f.Parse(args[1:]); err != nil || f.NArg() != 0 || *timeout <= 0 {
		return 2
	}
	if !filepath.IsAbs(*root) {
		return publishError("publish", errors.New("--config-root must be absolute"))
	}
	dir, err := filepath.EvalSymlinks(filepath.Join(*root, name))
	if err != nil {
		return publishError("publish state", err)
	}
	if err = privatefile.Verify(dir); err != nil {
		return publishError("publish state", err)
	}
	lock, err := acquireFileLock(filepath.Join(dir, "config.lock"))
	if err != nil {
		return publishError("publish", err)
	}
	defer lock.Close()
	var job publishJob
	if err = readPublishJSON(filepath.Join(dir, "job.json"), &job); err != nil {
		return publishError("publish state", err)
	}
	if job.Name != name {
		return publishError("publish state", errors.New("job name mismatch"))
	}
	if err = job.validateBinding(); err != nil {
		return publishError("publish state", err)
	}
	if err = privatefile.Verify(filepath.Join(dir, "job.json")); err != nil {
		return publishError("publish state", err)
	}
	actualSource, err := filepath.EvalSymlinks(job.Source)
	if err != nil || actualSource != job.Source {
		return publishError("publish source", errors.New("saved source path no longer resolves to the configured directory"))
	}
	p, err := loadPublishActivation(job.Profile, job.Activation)
	if err != nil {
		return publishError("publish activation", err)
	}
	if p.ClientID != job.ClientID || p.ServerID != job.ServerID {
		return publishError("publish activation", errors.New("activation identity changed; configure another name"))
	}
	cfg, err := job.config(p, dir)
	if err != nil {
		return publishError("publish configuration", err)
	}
	if err = checkNWCSource(job.Source, dir); err != nil {
		return publishError("publish source", err)
	}
	if err = lockPublishSandbox(cfg); err != nil {
		return publishError("publish sandbox", err)
	}
	if err = prepareSSHPath(cfg); err != nil {
		return publishError("publish tools", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	fmt.Fprintln(os.Stderr, "publish: refreshing repository access")
	view, err := fetchPublishView(ctx, cfg, p)
	if err != nil {
		return publishError("publish projection", err)
	}
	if view.RealmID != job.RealmID || view.Generation < job.MinimumGeneration {
		return publishError("publish projection", errors.New("projection identity changed or generation rolled back"))
	}
	r, err := selectPublishRepository(view, "", job.RepoID, p)
	if err != nil {
		return publishError("publish repository", err)
	}
	if r.URL != job.RepoURL {
		return publishError("publish repository", errors.New("repository URL changed; configure another name"))
	}
	if _, err = clientview.StoreIfNewer(filepath.Join(dir, "view.json"), view); err != nil {
		return publishError("publish projection", err)
	}
	remaining := *timeout
	if deadline, ok := ctx.Deadline(); ok {
		remaining = time.Until(deadline)
	}
	if remaining <= 0 {
		return publishError("publish", context.DeadlineExceeded)
	}
	return cmdSyncNoWC(cfg, cfg.Copies[name], runFlags{message: *message, timeout: remaining, noWorkingCopy: true, sandboxLocked: true, progress: os.Stderr})
}

func publishError(stage string, err error) int { fmt.Fprintln(os.Stderr, stage+":", err); return 1 }

func (j publishJob) config(p clientprofile.Profile, dir string) (Config, error) {
	if j.Schema != publishJobSchema || !safeCopyID(j.Name) || !filepath.IsAbs(j.Source) {
		return Config{}, errors.New("invalid publish job")
	}
	host, _, err := deploy.NormalizeServerAddress(p.Address)
	if err != nil {
		return Config{}, err
	}
	spec := workingCopySpec{ID: j.Name, Path: j.Source, URL: j.RepoURL}
	if spec.URL == "" {
		spec.URL = p.ServiceURL
	}
	cfg := Config{SVN: j.SVN, SVNMucc: j.SVNMucc, SSH: j.SSH, Identity: p.IdentityFile, KnownHosts: p.KnownHosts, Host: host, Port: p.SSHPort, LogDir: dir, Copies: map[string]workingCopySpec{j.Name: spec}, Only: []string{j.Name}}
	return cfg, cfg.validate()
}

// No offline fallback, no service-WC update, and no unbounded command output.
func fetchPublishView(ctx context.Context, cfg Config, p clientprofile.Profile) (clientview.View, error) {
	host, _, err := deploy.NormalizeServerAddress(p.Address)
	if err != nil {
		return clientview.View{}, err
	}
	u, err := restrictedPublishURL(p.ServiceURL, "_filees-client", host)
	if err != nil {
		return clientview.View{}, err
	}
	rel := filepath.ToSlash(p.RelativeViewPath)
	if !safeNWCRel(rel) {
		return clientview.View{}, errors.New("unsafe projection path")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + rel
	u.RawPath = ""
	remote, err := newNWCRemote(cfg, p.ServiceURL)
	if err != nil {
		return clientview.View{}, err
	}
	var output boundedPublishView
	if err := remote.process(ctx, cfg.SVN, []string{"cat", "--non-interactive", "--no-auth-cache", u.String() + "@"}, &output); err != nil {
		return clientview.View{}, err
	}
	v, err := clientview.Decode(bytes.NewReader(output.Bytes()))
	if err == nil && v.ClientID != p.ClientID {
		err = errors.New("projection belongs to another activated client")
	}
	return v, err
}

type boundedPublishView struct{ bytes.Buffer }

func (b *boundedPublishView) Write(p []byte) (int, error) {
	if len(p) > (1<<20)-b.Len() {
		return 0, errors.New("projection exceeds 1 MiB")
	}
	return b.Buffer.Write(p)
}

func restrictedPublishURL(raw, user, host string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "svn+ssh" || u.User == nil || u.User.Username() != user || !strings.EqualFold(u.Hostname(), host) || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return nil, errors.New("repository transport does not match activated server")
	}
	if _, password := u.User.Password(); password {
		return nil, errors.New("repository URL contains a password")
	}
	return u, nil
}

func selectPublishRepository(v clientview.View, name, id string, p clientprofile.Profile) (clientview.Repository, error) {
	var matches []clientview.Repository
	for _, r := range v.Repositories {
		if id != "" && r.RepoID == id || id == "" && r.DisplayName == name {
			matches = append(matches, r)
		}
	}
	if len(matches) != 1 {
		return clientview.Repository{}, errors.New("repository selection is missing or ambiguous; autoconfig without --repo lists available names")
	}
	r := matches[0]
	if v.ClientRole != "normal" || r.Access != "rw" || r.State != "active" || r.RequiresLock() || r.Purpose != "" {
		return clientview.Repository{}, errors.New("publication requires an active writable ordinary repository without mandatory edit locks")
	}
	host, _, err := deploy.NormalizeServerAddress(p.Address)
	if err == nil {
		_, err = restrictedPublishURL(r.URL, "_filees-data", host)
	}
	return r, err
}

func readPublishJSON(path string, out any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return errors.New("state must be a regular file of at most 1 MiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, (1<<20)+1))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("state contains trailing data")
	}
	return nil
}

func storePublishJob(dir string, j publishJob, v clientview.View) error {
	path := filepath.Join(dir, "job.json")
	var old publishJob
	if err := readPublishJSON(path, &old); err == nil {
		j.MinimumGeneration = old.MinimumGeneration
		if !reflect.DeepEqual(j, old) {
			return errors.New("this name already binds another configuration; choose another --name")
		}
		if v.Generation < old.MinimumGeneration {
			return errors.New("projection generation rollback")
		}
		_, err = clientview.StoreIfNewer(filepath.Join(dir, "view.json"), v)
		return err
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := clientview.StoreIfNewer(filepath.Join(dir, "view.json"), v); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".publish-job-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(0o600); err != nil {
		return err
	}
	if err = privatefile.Harden(f.Name()); err != nil {
		return err
	}
	if _, err = f.Write(append(raw, '\n')); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	return durable.SyncDirectory(dir)
}

// Resolve missing parents before creating anything: state must never be
// created inside the source, including a symlinked ancestor of --config-root.
func preparePublishDirectory(root, name, source string) (string, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) == filepath.Dir(filepath.Clean(root)) || strings.ContainsAny(root, "\x00\r\n") || !safeCopyID(name) {
		return "", errors.New("invalid configuration directory or name")
	}
	probe := filepath.Clean(root)
	var missing []string
	for {
		if _, err := os.Lstat(probe); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		missing = append(missing, filepath.Base(probe))
		probe = filepath.Dir(probe)
	}
	actual, err := filepath.EvalSymlinks(probe)
	if err != nil {
		return "", err
	}
	for i := len(missing) - 1; i >= 0; i-- {
		actual = filepath.Join(actual, missing[i])
	}
	if rel, err := filepath.Rel(source, actual); err == nil && (rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return "", errors.New("configuration directory must be outside the source")
	}
	if _, err = os.Lstat(actual); err == nil {
		if err = privatefile.Verify(actual); err != nil {
			return "", fmt.Errorf("existing configuration root must already be private: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err = privatefile.EnsureDir(actual); err != nil {
		return "", err
	}
	dir := filepath.Join(actual, name)
	if info, err := os.Lstat(dir); err == nil && !info.IsDir() {
		return "", errors.New("job state must be a real directory")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	} else if err == nil {
		if err = privatefile.Verify(dir); err != nil {
			return "", err
		}
	}
	if err = privatefile.EnsureDir(dir); err != nil {
		return "", err
	}
	return dir, nil
}

func loadPublishActivation(profilePath, activation string) (clientprofile.Profile, error) {
	if profilePath != "" && activation == "" && filepath.IsAbs(profilePath) {
		return clientprofile.Load(profilePath)
	}
	if profilePath != "" || !filepath.IsAbs(activation) {
		return clientprofile.Profile{}, errors.New("select either an absolute --profile or --activation")
	}
	var passport deploy.OnboardPassport
	if err := readPublishJSON(filepath.Join(activation, "onboard.json"), &passport); err != nil {
		return clientprofile.Profile{}, err
	}
	if passport.Schema != deploy.OnboardPassportSchema || passport.State != "accepted" {
		return clientprofile.Profile{}, errors.New("activation passport is not accepted")
	}
	identity, err := deploy.LoadActiveIdentity(filepath.Join(activation, "identity"))
	if err != nil {
		return clientprofile.Profile{}, err
	}
	host, port, err := deploy.NormalizeServerAddress(passport.ServerAddress)
	if err != nil {
		return clientprofile.Profile{}, err
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	p := clientprofile.Profile{Schema: clientprofile.Schema, ServerID: passport.ServerID, DisplayName: passport.ServerID, Address: passport.ServerAddress, ClientID: identity.ClientID, IdentityFile: identity.PrivateKeyPath, KnownHosts: passport.KnownHostsPath, SSHPort: port, ServiceURL: (&url.URL{Scheme: "svn+ssh", User: url.User("_filees-client"), Host: host, Path: "/clients/" + identity.ClientID + "/"}).String(), ServiceWC: filepath.Join(activation, "service-wc"), RelativeViewPath: "view.json", CachePath: filepath.Join(activation, "cache", "view.json"), PollInterval: time.Minute}
	return p, p.Validate()
}

func selectPublishActivation(pp, ap, root, server string) (clientprofile.Profile, string, string, error) {
	if pp != "" || ap != "" {
		if server != "" {
			return clientprofile.Profile{}, "", "", errors.New("--server cannot be combined with --profile or --activation")
		}
		p, err := loadPublishActivation(pp, ap)
		return p, pp, ap, err
	}
	if !filepath.IsAbs(root) {
		return clientprofile.Profile{}, "", "", errors.New("--state-root must be absolute")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return clientprofile.Profile{}, "", "", err
	}
	type candidate struct {
		p      clientprofile.Profile
		pp, ap string
	}
	var matches []candidate
	profiles, err := clientprofile.List(root)
	if err != nil {
		return clientprofile.Profile{}, "", "", err
	}
	for _, p := range profiles {
		dir, err := clientprofile.ServerDir(root, p.ServerID)
		if err != nil {
			return clientprofile.Profile{}, "", "", err
		}
		if server == "" || server == p.ServerID || server == p.DisplayName {
			matches = append(matches, candidate{p, filepath.Join(dir, "client-profile.json"), ""})
		}
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if _, err := os.Lstat(filepath.Join(dir, "client-profile.json")); !errors.Is(err, os.ErrNotExist) {
			continue
		}
		p, err := loadPublishActivation("", dir)
		if err != nil {
			continue
		} // pending activation and unrelated directories are not candidates
		expected, err := clientprofile.ServerDir(root, p.ServerID)
		if err != nil || filepath.Clean(expected) != filepath.Clean(dir) {
			return clientprofile.Profile{}, "", "", errors.New("activation directory does not match server identity")
		}
		if server == "" || server == p.ServerID || server == p.DisplayName {
			matches = append(matches, candidate{p, "", dir})
		}
	}
	if len(matches) != 1 {
		return clientprofile.Profile{}, "", "", errors.New("activation selection is missing or ambiguous; use --server NAME, --profile or --activation")
	}
	c := matches[0]
	return c.p, c.pp, c.ap, nil
}

// Reject malformed persisted repository identities before contacting any URL.
func (j publishJob) validateBinding() error {
	for _, id := range []string{j.ClientID, j.RealmID, j.RepoID} {
		if _, err := uuid.Parse(id); err != nil {
			return errors.New("invalid saved publication identity")
		}
	}
	if j.MinimumGeneration < 1 {
		return errors.New("invalid saved projection generation")
	}
	return nil
}
