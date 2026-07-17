package updater

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"filees/internal/serverinstall/config"
	"filees/internal/serverinstall/manifest"
	"filees/internal/serverinstall/platform"
	"filees/internal/serverinstall/signify"
	"filees/internal/serverinstall/state"
	"filees/internal/serverinstall/svnfetch"
)

type Runner struct {
	Config   *config.Config
	Fetcher  svnfetch.Fetcher
	Verifier signify.Verifier
	Platform platform.Backend
	Out      io.Writer
	In       io.Reader
}

type Options struct {
	ReleaseID string
	DryRun    bool
	Yes       bool
}

type PurgeOptions struct {
	WipeState bool
	Yes       bool
}

type Plan struct {
	ReleaseID      string
	SVNRevision    string
	CurrentRelease string
	FirstInstall   bool
	Files          []FilePlan
	ConfigIssues   []ConfigIssue
	Orphans        []OrphanPlan
}

type FilePlan struct {
	Source     string
	Target     string
	Action     string
	CurrentSHA string
	NewSHA     string
	Mode       os.FileMode
}

type ConfigIssue struct {
	Severity string
	Config   string
	Path     string
	Key      string
	Message  string
}

type OrphanPlan struct {
	Target string
	Reason string
}

type StagedFile struct {
	ManifestFile manifest.File
	Target       string
	StagePath    string
	SHA256       string
	Mode         os.FileMode
}

func NewRunner(cfg *config.Config, fetcher svnfetch.Fetcher, plat platform.Backend, out io.Writer, in io.Reader) *Runner {
	if out == nil {
		out = io.Discard
	}
	if in == nil {
		in = strings.NewReader("")
	}
	return &Runner{
		Config:   cfg,
		Fetcher:  fetcher,
		Verifier: signify.CLI{Program: cfg.SignifyProgram},
		Platform: plat,
		Out:      out,
		In:       in,
	}
}

func (r *Runner) dirs() manifest.Dirs {
	cfg := r.Config
	return manifest.Dirs{
		SbinDir:      cfg.SbinDir,
		LibexecDir:   cfg.LibexecDir,
		SysconfDir:   cfg.SysconfDir,
		SSHDConfDir:  cfg.SSHDConfDir,
		SSHKeysDir:   cfg.SSHKeysDir,
		LoginConfDir: cfg.LoginConfDir,
	}
}

func (r *Runner) ResolveManifest(ctx context.Context, releaseID string) (*manifest.Manifest, error) {
	if strings.TrimSpace(releaseID) == "" {
		chPath := manifest.ChannelPath(r.Config.Channel)
		chData, err := r.Fetcher.Cat(ctx, chPath)
		if err != nil {
			return nil, err
		}
		if err := r.verifyDetached(ctx, chData, chPath); err != nil {
			return nil, err
		}
		ch, err := manifest.ParseChannel(chData)
		if err != nil {
			return nil, err
		}
		releaseID = ch.ReleaseID
		path := manifest.ExpandPlatform(ch.Manifest, r.Config.Platform)
		return r.fetchManifest(ctx, path, releaseID)
	}
	path := manifest.ReleaseManifestPath(strings.TrimSpace(releaseID), r.Config.Platform)
	return r.fetchManifest(ctx, path, releaseID)
}

func (r *Runner) fetchManifest(ctx context.Context, path, expectedRelease string) (*manifest.Manifest, error) {
	data, err := r.Fetcher.Cat(ctx, path)
	if err != nil {
		return nil, err
	}
	if err := r.verifyDetached(ctx, data, path); err != nil {
		return nil, err
	}
	m, err := manifest.Parse(data)
	if err != nil {
		return nil, err
	}
	if expectedRelease != "" && m.ReleaseID != expectedRelease {
		return nil, fmt.Errorf("manifest release_id %q does not match requested %q", m.ReleaseID, expectedRelease)
	}
	if m.Platform != r.Config.Platform {
		return nil, fmt.Errorf("manifest platform %q does not match local %q", m.Platform, r.Config.Platform)
	}
	m.BasePath = strings.Trim(strings.TrimSuffix(filepath.ToSlash(filepath.Dir(path)), "."), "/")
	return m, nil
}

func (r *Runner) verifyDetached(ctx context.Context, data []byte, path string) error {
	if !r.Config.VerifySignature {
		return nil
	}
	if len(r.Config.SignifyPubkey) == 0 {
		return fmt.Errorf("%s: verify_signature enabled but no release key in this build", path)
	}
	sig, err := r.Fetcher.Cat(ctx, manifest.SigPath(path))
	if err != nil {
		return fmt.Errorf("%s: fetch signature: %w", path, err)
	}

	verifyDir := filepath.Join(r.Config.StageDir, "verify")
	if err := os.MkdirAll(verifyDir, 0o755); err != nil {
		return fmt.Errorf("%s: prepare verify dir: %w", path, err)
	}
	tmp, err := os.MkdirTemp(verifyDir, "sig-*")
	if err != nil {
		return fmt.Errorf("%s: prepare verify dir: %w", path, err)
	}
	defer os.RemoveAll(tmp)

	pubkeyPath := filepath.Join(tmp, "release.pub")
	msgPath := filepath.Join(tmp, "message")
	sigPath := filepath.Join(tmp, "message.sig")
	for path, data := range map[string][]byte{pubkeyPath: r.Config.SignifyPubkey, msgPath: data, sigPath: sig} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return fmt.Errorf("write verify temp: %w", err)
		}
	}
	if err := r.Verifier.Verify(ctx, pubkeyPath, msgPath, sigPath); err != nil {
		return fmt.Errorf("%s: signature verification failed: %w", path, err)
	}
	fmt.Fprintf(r.Out, "SIGNATURE OK %s\n", path)
	return nil
}

func (r *Runner) BuildPlan(m *manifest.Manifest, st *state.State) (*Plan, error) {
	dirs := r.dirs()
	plan := &Plan{
		ReleaseID:      m.ReleaseID,
		SVNRevision:    m.SVNRevision,
		CurrentRelease: st.InstalledRelease,
		FirstInstall:   st.IsFirstInstall(),
	}
	for _, mf := range m.Files {
		target := manifest.ResolveTarget(dirs, mf.Target)
		mode, err := parseMode(mf.Mode, mf.Kind)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", mf.Target, err)
		}
		fp := FilePlan{
			Source: mf.Source,
			Target: target,
			NewSHA: strings.ToLower(strings.TrimSpace(mf.SHA256)),
			Mode:   mode,
		}
		if sha, ok, err := fileSHA256(target); err != nil {
			return nil, err
		} else if !ok {
			fp.Action = "ADD"
		} else {
			fp.CurrentSHA = sha
			if fp.NewSHA != "" && strings.EqualFold(sha, fp.NewSHA) {
				fp.Action = "UNCHANGED"
			} else {
				fp.Action = "UPDATE"
			}
		}
		plan.Files = append(plan.Files, fp)
	}
	for _, cc := range m.Configs {
		plan.ConfigIssues = append(plan.ConfigIssues, checkConfigContract(cc)...)
	}
	for _, o := range m.Orphans {
		plan.Orphans = append(plan.Orphans, OrphanPlan{
			Target: manifest.ResolveOrphanTarget(dirs, o),
			Reason: o.Reason,
		})
	}
	return plan, nil
}

func (r *Runner) Check(ctx context.Context, opts Options) error {
	st, err := state.Load(r.Config.StateDir)
	if err != nil {
		return err
	}
	m, err := r.ResolveManifest(ctx, opts.ReleaseID)
	if err != nil {
		return err
	}
	base, err := r.baseUnveils()
	if err != nil {
		return err
	}
	if err := r.applyUnveils(append(base, r.manifestUnveils(m, false)...)); err != nil {
		return err
	}
	plan, err := r.BuildPlan(m, st)
	if err != nil {
		return err
	}
	r.PrintPlan(plan)
	return nil
}

func (r *Runner) Apply(ctx context.Context, opts Options) error {
	st, err := state.Load(r.Config.StateDir)
	if err != nil {
		return err
	}
	m, err := r.ResolveManifest(ctx, opts.ReleaseID)
	if err != nil {
		return err
	}

	var staged []StagedFile
	if !opts.DryRun {
		staged, _, err = r.stageFiles(ctx, m)
		if err != nil {
			return err
		}
	}

	// System runs (first-install, or an upgrade that touches the sshd
	// fragment) must exec OS tools after this point, and unveil state is
	// inherited by exec'd children — so those runs skip unveil entirely and
	// keep the bootstrap pledge until their system phase completes.
	// File-only runs (including every dry-run) get the full profile.
	systemRun := !opts.DryRun && (st.IsFirstInstall() || r.manifestTouchesSSHD(m))
	if systemRun {
		if r.Config.Talkative {
			fmt.Fprintln(r.Out, "[SECURITY] system run: unveil skipped, bootstrap pledge retained")
		}
	} else {
		base, err := r.baseUnveils()
		if err != nil {
			return err
		}
		if err := r.applyUnveils(append(base, r.manifestUnveils(m, !opts.DryRun)...)); err != nil {
			return err
		}
	}

	plan, err := r.BuildPlan(m, st)
	if err != nil {
		return err
	}
	r.PrintPlan(plan)
	if opts.DryRun {
		return nil
	}
	if err := r.confirmConfigDrift(plan, opts); err != nil {
		return err
	}

	// Files first: system tasks read the installed sshd/login.conf
	// fragments, so they must already be on disk.
	entry, err := r.installStaged(staged, st, m.ReleaseID, m.Orphans)
	if err != nil {
		return err
	}

	var sysSt *state.SystemState
	if st.IsFirstInstall() {
		sysSt, err = r.runSystemTasks(m)
		if err != nil {
			return err
		}
	} else if systemRun && r.sshdFragmentUpdated(plan) {
		if err := r.Platform.ReloadSSHD(); err != nil {
			return fmt.Errorf("reload sshd after fragment update: %w", err)
		}
	}
	if systemRun {
		// System phase over; drop proc+exec for the state write below.
		if err := r.reducePledge(); err != nil {
			return err
		}
	}

	st.InstalledRelease = m.ReleaseID
	st.InstalledAt = entry.InstalledAt
	if sysSt != nil {
		st.System = sysSt
	}
	st.History = append(st.History, entry)
	if err := state.Save(r.Config.StateDir, st); err != nil {
		return err
	}
	fmt.Fprintf(r.Out, "[UP] installed release=%s files=%d\n", m.ReleaseID, len(staged))
	return nil
}

// manifestTouchesSSHD reports whether any manifest file installs under the
// sshd config fragment directory. Decides the sandbox profile, so it must
// be computable from the manifest alone, before any unveil call.
func (r *Runner) manifestTouchesSSHD(m *manifest.Manifest) bool {
	dirs := r.dirs()
	for _, mf := range m.Files {
		if strings.HasPrefix(manifest.ResolveTarget(dirs, mf.Target), r.Config.SSHDConfDir) {
			return true
		}
	}
	return false
}

func (r *Runner) runSystemTasks(m *manifest.Manifest) (*state.SystemState, error) {
	users := []string{"_filees-state", "_filees-onboard", "_filees-tunnel"}
	created, err := r.Platform.EnsureUsers(users)
	if err != nil {
		return nil, fmt.Errorf("users: %w", err)
	}
	fmt.Fprintf(r.Out, "[SYS] users created: %v\n", created)

	if err := r.Platform.ApplyStateDirs("_filees-state"); err != nil {
		return nil, fmt.Errorf("state dirs: %w", err)
	}
	fmt.Fprintln(r.Out, "[SYS] state directories ready")

	if err := r.Platform.GenerateSecrets(r.Config.SysconfDir); err != nil {
		return nil, fmt.Errorf("secrets: %w", err)
	}
	fmt.Fprintln(r.Out, "[SYS] secrets generated")

	dirs := r.dirs()
	sysSt := &state.SystemState{UsersCreated: created}

	// The sshd and login.conf fragments were already installed by
	// installStaged as regular manifest files; here only the system-level
	// activation happens: the Include directive and cap_mkdb.
	for _, mf := range m.Files {
		target := manifest.ResolveTarget(dirs, mf.Target)
		switch {
		case strings.HasPrefix(target, r.Config.SSHDConfDir):
			backup, err := r.Platform.EnsureSSHDInclude(
				r.Config.SSHDConfDir, filepath.Join(r.Config.SSHKeysDir, "sshd_config"))
			if err != nil {
				return nil, fmt.Errorf("sshd include: %w", err)
			}
			sysSt.SSHDFragment = target
			sysSt.SSHDBackup = backup
		case strings.HasPrefix(target, r.Config.LoginConfDir):
			if err := r.Platform.RegisterLoginConf(target); err != nil {
				return nil, fmt.Errorf("login.conf: %w", err)
			}
			sysSt.LoginConf = target
		}
	}

	if err := r.Platform.ReloadSSHD(); err != nil {
		return nil, fmt.Errorf("reload sshd: %w", err)
	}
	fmt.Fprintln(r.Out, "[SYS] sshd reloaded")

	// Collect setuid binaries from manifest for informational tracking.
	for _, mf := range m.Files {
		mode, _ := parseMode(mf.Mode, mf.Kind)
		if mode&0o4000 != 0 {
			sysSt.SetuidBins = append(sysSt.SetuidBins, manifest.ResolveTarget(dirs, mf.Target))
		}
	}
	return sysSt, nil
}

func (r *Runner) sshdFragmentUpdated(plan *Plan) bool {
	for _, f := range plan.Files {
		if strings.HasPrefix(f.Target, r.Config.SSHDConfDir) &&
			(f.Action == "UPDATE" || f.Action == "ADD") {
			return true
		}
	}
	return false
}

func (r *Runner) Rollback() error {
	st, err := state.Load(r.Config.StateDir)
	if err != nil {
		return err
	}
	if len(st.History) == 0 {
		return fmt.Errorf("no install history to roll back")
	}
	entry := st.History[len(st.History)-1]
	base, err := r.baseUnveils()
	if err != nil {
		return err
	}
	if err := r.applyUnveils(append(base, rollbackUnveils(entry)...)); err != nil {
		return err
	}
	sshdTouched := false
	for _, bf := range entry.Files {
		if strings.HasPrefix(bf.Target, r.Config.SSHDConfDir) {
			sshdTouched = true
		}
	}
	for _, bf := range entry.Files {
		if bf.Existed {
			if err := installFile(bf.BackupPath, bf.Target, 0); err != nil {
				return fmt.Errorf("restore %s: %w", bf.Target, err)
			}
			fmt.Fprintf(r.Out, "RESTORE %s\n", bf.Target)
			continue
		}
		if err := os.Remove(bf.Target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", bf.Target, err)
		}
		fmt.Fprintf(r.Out, "REMOVE %s\n", bf.Target)
	}
	st.InstalledRelease = entry.PreviousRelease
	st.InstalledAt = ""
	st.History = st.History[:len(st.History)-1]
	if err := state.Save(r.Config.StateDir, st); err != nil {
		return err
	}
	if sshdTouched {
		// Rollback is a file-only run (sandboxed without exec), so sshd is
		// deliberately not reloaded here; a rollback must stay predictable
		// even when the sshd config is the thing being recovered from.
		fmt.Fprintln(r.Out, "WARN: sshd fragment restored; run 'rcctl reload sshd' (or systemctl) manually")
	}
	fmt.Fprintf(r.Out, "[UP] rollback ok release=%s\n", emptyDash(st.InstalledRelease))
	return nil
}

func (r *Runner) Purge(opts PurgeOptions) error {
	st, err := state.Load(r.Config.StateDir)
	if err != nil {
		return err
	}
	if st.System == nil && len(st.History) == 0 {
		return fmt.Errorf("nothing installed (no state found)")
	}

	// Purge is a system run: it execs userdel, cap_mkdb, sshd and rcctl,
	// whose children would inherit any unveil profile — so no unveil; the
	// bootstrap pledge (with proc exec) stays for the whole purge.
	if r.Config.Talkative {
		fmt.Fprintln(r.Out, "[SECURITY] system run: unveil skipped, bootstrap pledge retained")
	}

	r.printPurgeWarning(st, opts)
	if !opts.Yes {
		if !r.confirmInteractive("Proceed with purge? [y/N] ") {
			return fmt.Errorf("purge cancelled")
		}
	}

	// 1. Remove all installed files from all history entries.
	for _, entry := range st.History {
		for _, bf := range entry.Files {
			if err := os.Remove(bf.Target); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove %s: %w", bf.Target, err)
			}
			fmt.Fprintf(r.Out, "REMOVE %s\n", bf.Target)
		}
	}

	if st.System != nil {
		// 2. Remove sshd fragment and restore backup.
		if st.System.SSHDFragment != "" {
			if err := os.Remove(st.System.SSHDFragment); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove sshd fragment: %w", err)
			}
		}
		if st.System.SSHDBackup != "" {
			sshdConfig := filepath.Join(r.Config.SSHKeysDir, "sshd_config")
			if err := installFile(st.System.SSHDBackup, sshdConfig, 0); err != nil {
				return fmt.Errorf("restore sshd_config: %w", err)
			}
			fmt.Fprintf(r.Out, "RESTORE %s\n", sshdConfig)
		}
		if err := r.Platform.ReloadSSHD(); err != nil {
			fmt.Fprintf(r.Out, "WARN: sshd reload failed: %v\n", err)
		}

		// 3. Remove login.conf fragment.
		if st.System.LoginConf != "" {
			if err := r.Platform.RemoveLoginConf(st.System.LoginConf); err != nil {
				return fmt.Errorf("remove login.conf: %w", err)
			}
		}

		// 4. Remove users created by this installer.
		for _, name := range st.System.UsersCreated {
			if err := r.Platform.RemoveUser(name); err != nil {
				return fmt.Errorf("remove user %s: %w", name, err)
			}
			fmt.Fprintf(r.Out, "REMOVE-USER %s\n", name)
		}
	}

	// 5. Remove state.json.
	if err := os.Remove(state.Path(r.Config.StateDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove state.json: %w", err)
	}

	if opts.WipeState {
		for _, dir := range []string{r.Config.DataDir, r.Config.SysconfDir} {
			if err := os.RemoveAll(dir); err != nil {
				return fmt.Errorf("wipe %s: %w", dir, err)
			}
			fmt.Fprintf(r.Out, "WIPE %s\n", dir)
		}
	} else {
		fmt.Fprintf(r.Out, "WARN: %s and %s were NOT removed.\n",
			r.Config.DataDir, r.Config.SysconfDir)
		fmt.Fprintln(r.Out, "      Use --wipe-state to delete all operational state (irreversible).")
	}
	fmt.Fprintln(r.Out, "[UP] purge complete")
	return nil
}

func (r *Runner) printPurgeWarning(st *state.State, opts PurgeOptions) {
	fmt.Fprintln(r.Out, "=== PURGE PLAN ===")
	for _, entry := range st.History {
		for _, bf := range entry.Files {
			fmt.Fprintf(r.Out, "  REMOVE %s\n", bf.Target)
		}
	}
	if st.System != nil {
		if st.System.SSHDFragment != "" {
			fmt.Fprintf(r.Out, "  REMOVE sshd fragment %s\n", st.System.SSHDFragment)
		}
		if st.System.SSHDBackup != "" {
			fmt.Fprintf(r.Out, "  RESTORE sshd_config from %s\n", st.System.SSHDBackup)
		}
		if st.System.LoginConf != "" {
			fmt.Fprintf(r.Out, "  REMOVE login.conf %s\n", st.System.LoginConf)
		}
		for _, name := range st.System.UsersCreated {
			fmt.Fprintf(r.Out, "  REMOVE-USER %s\n", name)
		}
	}
	if opts.WipeState {
		fmt.Fprintf(r.Out, "  WIPE %s (operational state — irreversible)\n", r.Config.DataDir)
		fmt.Fprintf(r.Out, "  WIPE %s (config — irreversible)\n", r.Config.SysconfDir)
	} else {
		fmt.Fprintf(r.Out, "  KEEP %s  (use --wipe-state to remove)\n", r.Config.DataDir)
		fmt.Fprintf(r.Out, "  KEEP %s  (use --wipe-state to remove)\n", r.Config.SysconfDir)
	}
	fmt.Fprintln(r.Out, "==================")
}

func (r *Runner) PrintPlan(plan *Plan) {
	if plan.SVNRevision != "" {
		fmt.Fprintf(r.Out, "[UP] installed=%s available=%s svn_revision=%s", emptyDash(plan.CurrentRelease), plan.ReleaseID, plan.SVNRevision)
	} else {
		fmt.Fprintf(r.Out, "[UP] installed=%s available=%s", emptyDash(plan.CurrentRelease), plan.ReleaseID)
	}
	if plan.FirstInstall {
		fmt.Fprint(r.Out, " (first install)")
	}
	fmt.Fprintln(r.Out)
	for _, f := range plan.Files {
		if f.NewSHA != "" {
			fmt.Fprintf(r.Out, "%s %s sha256=%s\n", f.Action, f.Target, shortSHA(f.NewSHA))
		} else {
			fmt.Fprintf(r.Out, "%s %s sha256=MISSING\n", f.Action, f.Target)
		}
	}
	for _, issue := range plan.ConfigIssues {
		fmt.Fprintf(r.Out, "CONFIG %s %s %s\n", issue.Severity, issue.Path, issue.Message)
	}
	for _, o := range plan.Orphans {
		if o.Reason != "" {
			fmt.Fprintf(r.Out, "ORPHAN %s (%s)\n", o.Target, o.Reason)
		} else {
			fmt.Fprintf(r.Out, "ORPHAN %s\n", o.Target)
		}
	}
}

func (r *Runner) confirmConfigDrift(plan *Plan, opts Options) error {
	if r.Config.ConfigDrift == "ignore" {
		return nil
	}
	blocked := false
	for _, issue := range plan.ConfigIssues {
		if issue.Severity == "FAIL" && r.Config.ConfigDrift == "block" {
			blocked = true
			break
		}
	}
	if !blocked {
		return nil
	}
	if opts.Yes {
		return nil
	}
	if !r.Config.Interactive {
		return fmt.Errorf("configuration merge needed before upgrade")
	}
	fmt.Fprint(r.Out, "New required config option detected; merge needed after upgrade, proceed? [y/N] ")
	if !r.confirmInteractive("") {
		return fmt.Errorf("upgrade cancelled")
	}
	return nil
}

func (r *Runner) confirmInteractive(prompt string) bool {
	if prompt != "" {
		fmt.Fprint(r.Out, prompt)
	}
	sc := bufio.NewScanner(r.In)
	if !sc.Scan() {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(sc.Text()))
	return answer == "y" || answer == "yes"
}

func (r *Runner) stageFiles(ctx context.Context, m *manifest.Manifest) ([]StagedFile, string, error) {
	stageRoot := filepath.Join(r.Config.StageDir, m.ReleaseID+"-"+m.Platform+"-"+state.NowStamp())
	if err := os.MkdirAll(stageRoot, 0o755); err != nil {
		return nil, "", err
	}
	dirs := r.dirs()
	var staged []StagedFile
	for _, mf := range m.Files {
		if r.Config.RequireHash && strings.TrimSpace(mf.SHA256) == "" {
			return nil, "", fmt.Errorf("%s: sha256 required by policy", mf.Source)
		}
		data, err := r.Fetcher.Cat(ctx, m.SourcePath(mf.Source))
		if err != nil {
			return nil, "", err
		}
		sum := sha256.Sum256(data)
		got := hex.EncodeToString(sum[:])
		want := strings.ToLower(strings.TrimSpace(mf.SHA256))
		if want != "" && got != want {
			return nil, "", fmt.Errorf("%s: sha256 mismatch got=%s want=%s", mf.Source, got, want)
		}
		stagePath := filepath.Join(stageRoot, filepath.FromSlash(strings.TrimLeft(mf.Source, "/")))
		if err := os.MkdirAll(filepath.Dir(stagePath), 0o755); err != nil {
			return nil, "", err
		}
		mode, err := parseMode(mf.Mode, mf.Kind)
		if err != nil {
			return nil, "", fmt.Errorf("%s: %w", mf.Source, err)
		}
		if err := os.WriteFile(stagePath, data, mode); err != nil {
			return nil, "", err
		}
		target := manifest.ResolveTarget(dirs, mf.Target)
		staged = append(staged, StagedFile{
			ManifestFile: mf,
			Target:       target,
			StagePath:    stagePath,
			SHA256:       got,
			Mode:         mode,
		})
		fmt.Fprintf(r.Out, "FETCH %s sha256=%s\n", mf.Source, shortSHA(got))
	}
	return staged, stageRoot, nil
}

func (r *Runner) installStaged(staged []StagedFile, st *state.State, releaseID string, orphans []manifest.Orphan) (state.HistoryEntry, error) {
	stamp := time.Now().UTC().Format(time.RFC3339)
	backupRoot := filepath.Join(r.Config.BackupDir, state.NowStamp())
	entry := state.HistoryEntry{
		ReleaseID:       releaseID,
		PreviousRelease: st.InstalledRelease,
		InstalledAt:     stamp,
		BackupDir:       backupRoot,
	}
	dirs := r.dirs()
	for _, sf := range staged {
		bf := state.BackupFile{Target: sf.Target}
		if sha, ok, err := fileSHA256(sf.Target); err != nil {
			return entry, err
		} else if ok {
			bf.Existed = true
			bf.SHA256Before = sha
			rel := cleanBackupRel(sf.Target)
			bf.BackupPath = filepath.Join(backupRoot, rel)
			if err := copyFile(sf.Target, bf.BackupPath, 0); err != nil {
				return entry, err
			}
		}
		if err := installFile(sf.StagePath, sf.Target, sf.Mode); err != nil {
			return entry, err
		}
		entry.Files = append(entry.Files, bf)
		fmt.Fprintf(r.Out, "INSTALL %s\n", sf.Target)
	}
	if r.Config.OrphanFiles == "remove" {
		for _, orphan := range orphans {
			target := manifest.ResolveOrphanTarget(dirs, orphan)
			sha, ok, err := fileSHA256(target)
			if err != nil {
				return entry, err
			}
			if !ok {
				continue
			}
			bf := state.BackupFile{
				Target:       target,
				Existed:      true,
				SHA256Before: sha,
				BackupPath:   filepath.Join(backupRoot, cleanBackupRel(target)),
			}
			if err := copyFile(target, bf.BackupPath, 0); err != nil {
				return entry, err
			}
			if err := os.Remove(target); err != nil {
				return entry, err
			}
			entry.Files = append(entry.Files, bf)
			fmt.Fprintf(r.Out, "REMOVE-ORPHAN %s\n", target)
		}
	}
	return entry, nil
}

// --- helpers ---

func checkConfigContract(cc manifest.ConfigContract) []ConfigIssue {
	path := strings.TrimSpace(cc.Path)
	keys, err := readConfigKeys(path)
	if err != nil {
		return []ConfigIssue{{Severity: "FAIL", Config: cc.Name, Path: path, Message: "config file not readable"}}
	}
	var issues []ConfigIssue
	for _, key := range cc.RequiredKeys {
		if _, ok := keys[norm(key)]; !ok {
			issues = append(issues, ConfigIssue{Severity: "FAIL", Config: cc.Name, Path: path, Key: key, Message: "missing required key " + key})
		}
	}
	for _, key := range cc.RecommendedKeys {
		if _, ok := keys[norm(key)]; !ok {
			issues = append(issues, ConfigIssue{Severity: "WARN", Config: cc.Name, Path: path, Key: key, Message: "missing recommended key " + key})
		}
	}
	for _, key := range append(cc.DeprecatedKeys, cc.RemovedKeys...) {
		if _, ok := keys[norm(key)]; ok {
			issues = append(issues, ConfigIssue{Severity: "WARN", Config: cc.Name, Path: path, Key: key, Message: "deprecated key present " + key})
		}
	}
	for _, dc := range cc.DefaultChanged {
		if val, ok := keys[norm(dc.Key)]; ok && dc.Old != "" && val == dc.Old {
			issues = append(issues, ConfigIssue{Severity: "WARN", Config: cc.Name, Path: path, Key: dc.Key,
				Message: "default changed for " + dc.Key + " from " + dc.Old + " to " + dc.New})
		}
	}
	return issues
}

func readConfigKeys(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	keys := map[string]string{}
	section := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if section != "" && !strings.Contains(key, ".") {
			key = section + "." + key
		}
		keys[norm(key)] = strings.TrimSpace(val)
	}
	return keys, sc.Err()
}

func norm(key string) string { return strings.ToLower(strings.TrimSpace(key)) }

func fileSHA256(path string) (string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", false, err
	}
	return hex.EncodeToString(h.Sum(nil)), true, nil
}

func parseMode(raw, kind string) (os.FileMode, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if kind == "binary" {
			return 0o755, nil
		}
		return 0o644, nil
	}
	n, err := strconv.ParseUint(raw, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("bad mode %q", raw)
	}
	return os.FileMode(n), nil
}

// tempInstallPath returns a deterministic PID-based temp sibling for dst.
// Using a fixed name (not os.CreateTemp's random suffix) lets the sandbox
// unveil the exact path before knowing install order.
func tempInstallPath(dst string) string {
	return filepath.Join(filepath.Dir(dst), "."+filepath.Base(dst)+
		fmt.Sprintf(".filees-install.%d.tmp", os.Getpid()))
}

func installFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := tempInstallPath(dst)
	if err := copyFile(src, tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	if mode == 0 {
		// Keep setuid/setgid/sticky, not just Perm()'s 0777 slice: backups
		// and rollback restores of the setuid binaries must round-trip the
		// full mode or a rollback would silently strip suid.
		mode = st.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Chmod(dst, mode)
}

func cleanBackupRel(target string) string {
	target = filepath.Clean(target)
	target = strings.TrimPrefix(target, filepath.VolumeName(target))
	target = strings.TrimLeft(target, string(filepath.Separator))
	if target == "" {
		target = "target"
	}
	return target
}

func shortSHA(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 12 {
		return s
	}
	return s[:12]
}

func emptyDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}
