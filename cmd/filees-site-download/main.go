// Command filees-site-download keeps the download page of filees.space in step
// with the signed release channel.
//
// Until it existed, a signed release reached the site because somebody copied
// an MSI there by hand, and the page named whatever was typed into it. Now
// signing and promoting a release is the publication: this program, run from
// cron on the web server, resolves the channel exactly as the desktop client's
// self-update does — envelope signature, manifest signature, identity binding,
// expiry — checks the installer against the signed manifest and swaps the
// download directory only when something changed. It never goes back to an
// older release, and on any error the previous page stays.
//
// The landing build runs the same program, so the page previewed locally and
// the page published by cron come from one implementation.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"filees/internal/releaseenvelope"
	"filees/internal/serverinstall/svnfetch"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "filees-site-download:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("filees-site-download", flag.ContinueOnError)
	configPath := flags.String("config", "", "download.json: channel, key ID and release notes")
	keyPath := flags.String("key", "", "signify public key of the release key (release-key.pub)")
	templatePath := flags.String("template", "", "download page template (download/index.html)")
	outDir := flags.String("out", "", "download directory to publish into")
	statePath := flags.String("state", "", "state file remembering the published release (outside the web root)")
	svnProgram := flags.String("svn", "svn", "svn command")
	quiet := flags.Bool("quiet", false, "print nothing when the page is already current")
	if err := flags.Parse(args); err != nil {
		return err
	}
	for name, value := range map[string]string{"-config": *configPath, "-key": *keyPath, "-template": *templatePath, "-out": *outDir, "-state": *statePath} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	config, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	keyData, err := os.ReadFile(*keyPath)
	if err != nil {
		return err
	}
	key, err := releaseenvelope.CanonicalSignifyPublicKey(keyData)
	if err != nil {
		return fmt.Errorf("%s: %w", *keyPath, err)
	}
	template, err := os.ReadFile(*templatePath)
	if err != nil {
		return err
	}
	out, err := filepath.Abs(*outDir)
	if err != nil {
		return err
	}
	state, err := filepath.Abs(*statePath)
	if err != nil {
		return err
	}
	fetcher := svnfetch.SVN{Program: *svnProgram, RepoURL: config.Repo, Timeout: 5 * time.Minute}
	publisher := Publisher{
		Resolver: &releaseenvelope.Resolver{
			Fetcher:     fetcher,
			Verifier:    releaseenvelope.Ed25519Verifier{Keys: map[string][]byte{config.KeyID: key}},
			TrustedKeys: []string{config.KeyID},
		},
		Fetcher:   fetcher,
		Dater:     svnDater{Program: *svnProgram, RepoURL: config.Repo},
		Config:    config,
		Template:  template,
		OutDir:    out,
		StatePath: state,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	result, err := publisher.Publish(ctx)
	if err != nil {
		return err
	}
	if result.Changed {
		fmt.Printf("published %s (%s, %s, sha256 %s)\n", result.Installer, result.Version, result.ReleaseID, result.SHA256)
	} else if !*quiet {
		fmt.Printf("up to date: %s (%s, %s)\n", result.Installer, result.Version, result.ReleaseID)
	}
	return nil
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func loadConfig(configPath string) (Config, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return Config{}, err
	}
	var config Config
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("%s: %w", configPath, err)
	}
	if !strings.HasPrefix(config.Repo, "svn://") && !strings.HasPrefix(config.Repo, "https://") && !strings.HasPrefix(config.Repo, "file://") {
		return Config{}, fmt.Errorf("%s: repo must be an svn://, https:// or file:// URL", configPath)
	}
	for name, value := range map[string]string{"channel": config.Channel, "component": config.Component, "platform": config.Platform, "key_id": config.KeyID} {
		if !identifier.MatchString(value) {
			return Config{}, fmt.Errorf("%s: %s %q is not a plain identifier", configPath, name, value)
		}
	}
	return config, nil
}

// svnDater reads the last-changed date of a path in the release repository.
type svnDater struct {
	Program string
	RepoURL string
}

func (d svnDater) LastChanged(ctx context.Context, repoPath string) (time.Time, error) {
	if strings.Contains(repoPath, "..") || strings.HasPrefix(repoPath, "/") {
		return time.Time{}, errors.New("unsafe repository path")
	}
	url := strings.TrimRight(d.RepoURL, "/") + "/" + repoPath
	command := exec.CommandContext(ctx, d.Program, "info", "--non-interactive", "--no-auth-cache", "--show-item", "last-changed-date", url)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return time.Time{}, fmt.Errorf("svn info %s: %w: %s", url, err, strings.TrimSpace(stderr.String()))
	}
	return time.Parse(time.RFC3339Nano, strings.TrimSpace(stdout.String()))
}
