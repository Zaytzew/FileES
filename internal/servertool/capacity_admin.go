package servertool

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filees/internal/obsandbox"
	"filees/internal/serveralerts"
	"filees/internal/storagewatch"
	"filees/pkg/activation"
	"filees/pkg/alertchannel"
	"filees/pkg/serverconfig"
	"filees/pkg/smtpsubmit"
	"github.com/google/uuid"
)

type capacityConfig struct {
	Schema   string              `json:"schema"`
	Realm    string              `json:"realm_id"`
	Email    string              `json:"admin_email"`
	StateDir string              `json:"state_dir"`
	Paths    []storagewatch.Path `json:"paths,omitempty"`
	Policy   storagewatch.Policy `json:"policy"`
}

func loadCapacityConfig(path string) (capacityConfig, error) {
	c := capacityConfig{Policy: storagewatch.DefaultPolicy()}
	f, e := os.Open(path)
	if e != nil {
		return c, e
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 65537))
	d.DisallowUnknownFields()
	if e = d.Decode(&c); e != nil {
		return c, e
	}
	if d.Decode(new(any)) != io.EOF {
		return c, errors.New("trailing capacity configuration")
	}
	if c.Schema != "filees.capacity-alerts/v1" || !filepath.IsAbs(c.StateDir) || len(c.Paths) > 32 {
		return c, errors.New("invalid capacity configuration")
	}
	if _, e = uuid.Parse(c.Realm); e != nil {
		return c, e
	}
	a, e := mail.ParseAddress(c.Email)
	if e != nil || a.Address != c.Email || strings.ContainsAny(c.Email, "\r\n") {
		return c, errors.New("capacity admin_email must be a mailbox address")
	}
	for _, p := range c.Paths {
		if p.Label == "" || len(p.Label) > 64 || strings.ContainsAny(p.Label, "\r\n\t") || !filepath.IsAbs(p.Name) {
			return c, errors.New("invalid extra capacity path")
		}
	}
	return c, c.Policy.Validate()
}
func capacityPaths(c serverconfig.Config) []storagewatch.Path {
	session := c.Activation.SessionRoot
	if session == "" {
		session = filepath.Join(c.Activation.Root, "sessions")
	}
	paths := []storagewatch.Path{{"repositories", c.Repositories.Root}, {"operations", c.Repositories.ResultsRoot}, {"onboarding", c.Root}, {"activation", c.Activation.Root}, {"sessions", session}, {"service-repo", c.Activation.ServiceRepository}, {"service-wc", c.Activation.ServiceWorkingCopy}, {"temporary", os.TempDir()}}
	add := func(label, path string) {
		if path != "" {
			paths = append(paths, storagewatch.Path{Label: label, Name: path})
		}
	}
	add("whale", c.Repositories.EffectiveWhaleRoot())
	add("deleted", c.Repositories.DeletionArchiveRoot)
	add("rotated", c.Repositories.RotationArchiveRoot)
	if c.PublicShares.Enabled {
		add("public-state", c.PublicShares.EffectiveStateRoot(c.Repositories.ResultsRoot))
		add("public-staging", c.PublicShares.EffectiveAuthorityStagingRoot())
	}
	if c.Upload.Enabled() {
		add("upload", c.Upload.IntakeRoot)
		add("upload-trash", c.Upload.EffectiveTrashRoot(c.Repositories.ResultsRoot))
	}
	return paths
}
func runAdminCapacity(path string, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("alert capacity", flag.ContinueOnError)
	flags.SetOutput(stderr)
	monitorPath := flags.String("settings", filepath.Join(filepath.Dir(path), "capacity-alerts.json"), "operator-owned capacity settings")
	scheduled := flags.Bool("scheduled", false, "quiet no-op until capacity settings exist")
	dry := flags.Bool("dry-run", false, "measure only; no state, mail or publication")
	if e := flags.Parse(args); e != nil || flags.NArg() != 0 {
		return adminUsage(stderr, flags, "[--settings path] [--dry-run]")
	}
	// SMTP and SVN are both required; children retain only SVN exec promises.
	if e := sandboxBegin(svnPromises + " inet dns"); e != nil {
		report(stderr, "capacity sandbox", e)
		return ExitSoftware
	}
	settings, e := loadCapacityConfig(*monitorPath)
	if *scheduled && errors.Is(e, os.ErrNotExist) {
		return ExitOK
	}
	if e != nil {
		report(stderr, "capacity settings", e)
		return ExitConfig
	}
	c, e := serverconfig.LoadFor(path, serverconfig.SecretActivation|serverconfig.SecretSMTP)
	if e != nil {
		report(stderr, "capacity config", e)
		return ExitConfig
	}
	selectedPaths := append(capacityPaths(c), settings.Paths...)
	selectedPaths = append(selectedPaths, storagewatch.Path{Label: "alert-state", Name: settings.StateDir})
	paths, e := storagewatch.Prepare(selectedPaths)
	if e != nil {
		report(stderr, "capacity paths", e)
		return ExitSoftware
	}
	if !*dry {
		if e = os.MkdirAll(settings.StateDir, 0700); e != nil {
			report(stderr, "capacity state", e)
			return ExitSoftware
		}
	}
	profile := repositoryProfile(c.Root, toolAccess{name: "filees-admin/capacity", write: true, needActivation: true, needSVN: true}, c.Activation, "", "", "")
	profile.Promises = svnPromises + " inet dns"
	profile.Paths = append(profile.Paths, obsandbox.Path{Label: "svnlook", Name: c.Repositories.EffectiveSVNLookBinary(), Perms: "rx"}, obsandbox.Path{Label: "svnmucc", Name: c.Repositories.EffectiveSVNMuccBinary(), Perms: "rx"}, obsandbox.Path{Label: "temporary", Name: os.TempDir(), Perms: "rwc"}, obsandbox.Path{Label: "resolver", Name: "/etc/resolv.conf", Perms: "r"}, obsandbox.Path{Label: "hosts", Name: "/etc/hosts", Perms: "r"})
	if !*dry {
		profile.Paths = append(profile.Paths, obsandbox.Path{Label: "capacity-state", Name: settings.StateDir, Perms: "rwc"})
	}
	for _, p := range paths {
		profile.Paths = append(profile.Paths, obsandbox.Path{Label: "capacity-" + p.Label, Name: p.Name, Perms: "r"})
	}
	if e = sandboxApplyForExec(profile, svnExecPromises); e != nil {
		report(stderr, "capacity sandbox", e)
		return ExitSoftware
	}
	if _, e = os.Stat(filepath.Join(c.Activation.ServiceWorkingCopy, "admin", "realms", settings.Realm+".json")); e != nil {
		report(stderr, "capacity recipient realm", e)
		return ExitConfig
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if !*dry {
		lock, e := storagewatch.Lock(filepath.Join(settings.StateDir, "capacity.lock"))
		if e != nil {
			report(stderr, "capacity lock", e)
			return ExitTempFail
		}
		defer lock.Close()
	}
	volumes, e := storagewatch.Collect(ctx, paths, storagewatch.Native{})
	if e != nil {
		report(stderr, "capacity measure", e)
		return ExitTempFail
	}
	if *dry {
		if e = json.NewEncoder(stdout).Encode(volumes); e != nil {
			return ExitSoftware
		}
		return ExitOK
	}
	statePath := filepath.Join(settings.StateDir, "capacity.json")
	state, e := storagewatch.Load(statePath, settings.Realm, settings.Email)
	if e != nil {
		report(stderr, "capacity state", e)
		return ExitSoftware
	}
	if e = state.Observe(volumes, settings.Policy, time.Now()); e != nil {
		report(stderr, "capacity observation", e)
		return ExitSoftware
	}
	// Persist the email intent before either delivery route. SMTP success is not
	// proof of mailbox delivery; a crash after SMTP acceptance may cause a retry.
	if e = state.Save(statePath); e != nil {
		report(stderr, "capacity persist", e)
		return ExitSoftware
	}
	failed := false
	// Email goes first and has an independent budget; service-repo failure cannot
	// swallow the fallback or consume all the available execution time.
	for len(state.Pending) > 0 {
		m := state.Pending[0]
		mailCtx, done := context.WithTimeout(ctx, 15*time.Second)
		message := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: FileES server capacity alert\r\nMessage-ID: <capacity-%s@%s>\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\nServer: %s\r\n%s\r\n", c.SMTPFrom, settings.Email, m.ID, c.MessageIDDomain, time.Now().Format(time.RFC1123Z), strings.ReplaceAll(c.ServerDisplayName, "\n", " "), m.Text)
		e = smtpSubmit(mailCtx, c.SMTP, smtpsubmit.Request{EnvelopeFrom: c.SMTPFrom, Recipient: settings.Email, Message: []byte(message)})
		done()
		if e != nil {
			report(stderr, "capacity email", e)
			failed = true
			break
		}
		state.Pending = state.Pending[1:]
		if e = state.Save(statePath); e != nil {
			report(stderr, "capacity mail receipt", e)
			failed = true
			break
		}
	}
	manager, e := activation.New(c.Activation, nil)
	if e == nil {
		e = manager.RefreshAlertAccess()
	}
	if e == nil {
		publisher := serveralerts.Publisher{Repository: c.Activation.ServiceRepository, SVNLook: c.Repositories.EffectiveSVNLookBinary(), SVNMucc: c.Repositories.EffectiveSVNMuccBinary()}
		_, e = publisher.Update(ctx, settings.Realm, func(s alertchannel.Snapshot) (alertchannel.Snapshot, bool, error) {
			return state.Project(s, time.Now())
		})
	}
	if e != nil {
		report(stderr, "capacity channel", e)
		failed = true
	}
	if failed {
		return ExitTempFail
	}
	return ExitOK
}
