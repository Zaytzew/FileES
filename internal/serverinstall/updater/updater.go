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

	"filees/internal/durable"
	"filees/internal/serverinstall/config"
	"filees/internal/serverinstall/manifest"
	"filees/internal/serverinstall/platform"
	"filees/internal/serverinstall/signify"
	"filees/internal/serverinstall/state"
	"filees/internal/serverinstall/svnfetch"
)

type Runner struct {
	Config    *config.Config
	Fetcher   svnfetch.Fetcher
	Verifier  signify.Verifier
	Platform  platform.Backend
	Ownership platform.OwnershipManager
	Out       io.Writer
	In        io.Reader
}

type Options struct {
	ReleaseID string
	DryRun    bool
	Yes       bool
	// AllowRollback deliberately installs a release older than the one already
	// recorded. Downgrading is sometimes legitimate (backing out a bad
	// release), but it must be an explicit administrative act: the whole point
	// of the freshness check is that a rollback must never be able to arrive
	// looking like an ordinary update.
	AllowRollback bool
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
	Owner      string
	Group      string
	UID        int
	GID        int
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
	Ownership    platform.Ownership
}

func NewRunner(cfg *config.Config, fetcher svnfetch.Fetcher, plat platform.Backend, out io.Writer, in io.Reader) *Runner {
	if out == nil {
		out = io.Discard
	}
	if in == nil {
		in = strings.NewReader("")
	}
	return &Runner{
		Config:    cfg,
		Fetcher:   fetcher,
		Verifier:  signify.CLI{Program: cfg.SignifyProgram},
		Platform:  plat,
		Ownership: platform.SystemOwnership{},
		Out:       out,
		In:        in,
	}
}

func (r *Runner) ownershipManager() platform.OwnershipManager {
	if r.Ownership != nil {
		return r.Ownership
	}
	return platform.SystemOwnership{}
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
	if !manifest.ValidIdentifier(r.Config.Platform) {
		return nil, fmt.Errorf("invalid platform %q", r.Config.Platform)
	}
	if strings.TrimSpace(releaseID) == "" {
		if !manifest.ValidIdentifier(r.Config.Channel) {
			return nil, fmt.Errorf("invalid channel name %q", r.Config.Channel)
		}
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
		m, err := r.fetchManifest(ctx, path, releaseID)
		if err != nil {
			return nil, err
		}
		// Both documents are signed separately, so both must agree on the
		// release's position in the ordering. A mismatch means one of them was
		// substituted, which is exactly what the freshness counters exist to
		// detect.
		if m.Sequence != ch.Sequence || m.SecurityEpoch != ch.SecurityEpoch {
			return nil, fmt.Errorf("channel and manifest disagree on release freshness: channel sequence=%d epoch=%d, manifest sequence=%d epoch=%d",
				ch.Sequence, ch.SecurityEpoch, m.Sequence, m.SecurityEpoch)
		}
		return m, nil
	}
	if !manifest.ValidIdentifier(releaseID) {
		return nil, fmt.Errorf("invalid release ID %q", releaseID)
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
	seenTargets := make(map[string]struct{}, len(m.Files)+len(m.Orphans))
	for _, mf := range m.Files {
		target := manifest.ResolveTarget(dirs, mf.Target)
		if _, exists := seenTargets[target]; exists {
			return nil, fmt.Errorf("manifest resolves more than one entry to target %s", target)
		}
		seenTargets[target] = struct{}{}
		mode, err := parseMode(mf.Mode, mf.Kind)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", mf.Target, err)
		}
		ownership, err := r.ownershipManager().Resolve(mf.Owner, mf.Group)
		if err != nil {
			return nil, fmt.Errorf("%s ownership: %w", mf.Target, err)
		}
		fp := FilePlan{
			Source: mf.Source,
			Target: target,
			NewSHA: strings.ToLower(strings.TrimSpace(mf.SHA256)),
			Mode:   mode,
			Owner:  mf.Owner,
			Group:  mf.Group,
			UID:    ownership.UID,
			GID:    ownership.GID,
		}
		if sha, ok, err := fileSHA256(target); err != nil {
			return nil, err
		} else if !ok {
			fp.Action = "ADD"
		} else {
			fp.CurrentSHA = sha
			if fp.NewSHA != "" && strings.EqualFold(sha, fp.NewSHA) {
				fp.Action = "UNCHANGED"
				info, err := os.Lstat(target)
				if err != nil {
					return nil, err
				}
				currentOwnership, err := r.ownershipManager().Stat(target)
				if err != nil {
					return nil, fmt.Errorf("stat ownership %s: %w", target, err)
				}
				currentMode := info.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
				if currentMode != mode || currentOwnership != ownership {
					fp.Action = "METADATA"
				}
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
		target := manifest.ResolveOrphanTarget(dirs, o)
		if _, exists := seenTargets[target]; exists {
			return nil, fmt.Errorf("orphan overlaps managed or duplicate target %s", target)
		}
		seenTargets[target] = struct{}{}
		plan.Orphans = append(plan.Orphans, OrphanPlan{
			Target: target,
			Reason: o.Reason,
		})
	}
	return plan, nil
}

func (r *Runner) Check(ctx context.Context, opts Options) error {
	lock, err := r.acquireRunLock()
	if err != nil {
		return err
	}
	defer lock.Close()

	if err := r.recoverInterrupted(); err != nil {
		return err
	}
	st, err := state.Load(r.Config.StateDir)
	if err != nil {
		return err
	}
	m, err := r.ResolveManifest(ctx, opts.ReleaseID)
	if err != nil {
		return err
	}
	if err := r.checkFreshness(m, st, opts); err != nil {
		return err
	}
	base, err := r.baseUnveils()
	if err != nil {
		return err
	}
	if err := r.applyUnveils(append(base, r.manifestUnveils(m, false)...)); err != nil {
		return err
	}
	if err := r.reducePledge(); err != nil {
		return err
	}
	plan, err := r.BuildPlan(m, st)
	if err != nil {
		return err
	}
	r.PrintPlan(plan)
	return nil
}

// Adopt verifies that an installation created by the legacy shell installers
// already matches a signed release byte-for-byte and metadata-for-metadata,
// then records that release as the managed baseline. It never downloads or
// modifies payload files and never runs first-install system tasks.
func (r *Runner) Adopt(ctx context.Context, opts Options) error {
	lock, err := r.acquireRunLock()
	if err != nil {
		return err
	}
	defer lock.Close()

	if err := r.recoverInterrupted(); err != nil {
		return err
	}
	st, err := state.Load(r.Config.StateDir)
	if err != nil {
		return err
	}
	if !st.CanAdopt() {
		return fmt.Errorf("cannot adopt: installer state already exists")
	}
	m, err := r.ResolveManifest(ctx, opts.ReleaseID)
	if err != nil {
		return err
	}
	if err := r.checkFreshness(m, st, opts); err != nil {
		return err
	}
	base, err := r.baseUnveils()
	if err != nil {
		return err
	}
	if err := r.applyUnveils(append(base, r.manifestUnveils(m, false)...)); err != nil {
		return err
	}
	if err := r.reducePledge(); err != nil {
		return err
	}
	plan, err := r.BuildPlan(m, st)
	if err != nil {
		return err
	}
	r.PrintPlan(plan)
	var problems []string
	for _, file := range plan.Files {
		if file.Action != "UNCHANGED" {
			problems = append(problems, fmt.Sprintf("%s is %s", file.Target, file.Action))
		}
	}
	for _, issue := range plan.ConfigIssues {
		if issue.Severity == "FAIL" {
			problems = append(problems, fmt.Sprintf("%s: %s", issue.Path, issue.Message))
		}
	}
	if len(problems) != 0 {
		return fmt.Errorf("cannot adopt release %s: local installation does not exactly match signed baseline: %s",
			m.ReleaseID, strings.Join(problems, "; "))
	}

	installedAt := time.Now().UTC().Format(time.RFC3339)
	st.InstalledRelease = m.ReleaseID
	st.InstalledAt = installedAt
	st.AdvanceFreshness(m.Sequence, m.SecurityEpoch)
	st.System = &state.SystemState{Adopted: true}
	if err := state.Save(r.Config.StateDir, st); err != nil {
		return err
	}
	fmt.Fprintf(r.Out, "[UP] adopted release=%s files=%d (no payload changed)\n", m.ReleaseID, len(plan.Files))
	return nil
}

// checkFreshness refuses a stale or replayed release before anything is
// fetched, staged or installed. AllowRollback turns the refusal into a loud,
// explicit override rather than silently skipping the check.
func (r *Runner) checkFreshness(m *manifest.Manifest, st *state.State, opts Options) error {
	err := st.CheckFreshness(m.Sequence, m.SecurityEpoch, m.ReleaseID)
	if err == nil {
		return nil
	}
	if opts.AllowRollback && errors.Is(err, state.ErrRollback) {
		fmt.Fprintf(r.Out, "[SECURITY] rollback explicitly allowed by operator: %v\n", err)
		return nil
	}
	return err
}

func (r *Runner) Apply(ctx context.Context, opts Options) error {
	lock, err := r.acquireRunLock()
	if err != nil {
		return err
	}
	defer lock.Close()

	if err := r.recoverInterrupted(); err != nil {
		return err
	}
	st, err := state.Load(r.Config.StateDir)
	if err != nil {
		return err
	}
	m, err := r.ResolveManifest(ctx, opts.ReleaseID)
	if err != nil {
		return err
	}
	// Before anything is staged or written: a stale release must never get as
	// far as touching the filesystem.
	if err := r.checkFreshness(m, st, opts); err != nil {
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
			fmt.Fprintln(r.Out, "[SECURITY] system run: unveil skipped")
		}
	} else {
		base, err := r.baseUnveils()
		if err != nil {
			return err
		}
		if err := r.applyUnveils(append(base, r.manifestUnveils(m, !opts.DryRun)...)); err != nil {
			return err
		}
		if opts.DryRun {
			if err := r.reducePledge(); err != nil {
				return err
			}
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
	// All inode and system mutations are complete. Only now may OpenBSD
	// pledge: its first call permanently disables setting set-id bits.
	if err := r.reducePledge(); err != nil {
		return err
	}

	st.InstalledRelease = m.ReleaseID
	st.InstalledAt = entry.InstalledAt
	// Advanced only now, after the install actually succeeded - never at
	// download or staging time, so a failed or abandoned update cannot raise
	// the floor and lock the machine out of a legitimate retry.
	st.AdvanceFreshness(m.Sequence, m.SecurityEpoch)
	if sysSt != nil {
		st.System = sysSt
	}
	st.History = append(st.History, entry)
	if err := state.Save(r.Config.StateDir, st); err != nil {
		return err
	}
	if err := state.RemoveTransaction(r.Config.StateDir); err != nil {
		return fmt.Errorf("remove committed transaction journal: %w", err)
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
	lock, err := r.acquireRunLock()
	if err != nil {
		return err
	}
	defer lock.Close()

	if err := r.recoverInterrupted(); err != nil {
		return err
	}
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
	if err := validateEntryBackups(entry); err != nil {
		return err
	}
	for _, bf := range entry.Files {
		if bf.Existed {
			if bf.UIDBefore == nil || bf.GIDBefore == nil {
				return fmt.Errorf("restore %s: backup has no recorded uid/gid", bf.Target)
			}
			mode, err := recordedBackupMode(bf)
			if err != nil {
				return err
			}
			ownership := platform.Ownership{UID: *bf.UIDBefore, GID: *bf.GIDBefore}
			if err := r.installFile(bf.BackupPath, bf.Target, mode, ownership); err != nil {
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
	if err := r.reducePledge(); err != nil {
		return err
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
	lock, err := r.acquireRunLock()
	if err != nil {
		return err
	}
	defer lock.Close()

	if err := r.recoverInterrupted(); err != nil {
		return err
	}
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
		fmt.Fprintln(r.Out, "[SECURITY] system run: unveil skipped")
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
			fmt.Fprintf(r.Out, "%s %s sha256=%s owner=%s group=%s mode=%s\n",
				f.Action, f.Target, shortSHA(f.NewSHA), f.Owner, f.Group, formatMode(f.Mode))
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
	if err := os.MkdirAll(r.Config.StageDir, 0o755); err != nil {
		return nil, "", err
	}
	stageRoot, err := os.MkdirTemp(r.Config.StageDir, m.ReleaseID+"-"+m.Platform+"-"+state.NowStamp()+"-")
	if err != nil {
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
		ownership, err := r.ownershipManager().Resolve(mf.Owner, mf.Group)
		if err != nil {
			return nil, "", fmt.Errorf("%s ownership: %w", mf.Source, err)
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
			Ownership:    ownership,
		})
		fmt.Fprintf(r.Out, "FETCH %s sha256=%s\n", mf.Source, shortSHA(got))
	}
	return staged, stageRoot, nil
}

func (r *Runner) installStaged(staged []StagedFile, st *state.State, releaseID string, orphans []manifest.Orphan) (state.HistoryEntry, error) {
	stamp := time.Now().UTC().Format(time.RFC3339)
	if err := os.MkdirAll(r.Config.BackupDir, 0o700); err != nil {
		return state.HistoryEntry{}, err
	}
	backupRoot, err := os.MkdirTemp(r.Config.BackupDir, state.NowStamp()+"-")
	if err != nil {
		return state.HistoryEntry{}, err
	}
	if err := os.Chmod(backupRoot, 0o700); err != nil {
		return state.HistoryEntry{}, err
	}
	entry := state.HistoryEntry{
		ReleaseID:       releaseID,
		PreviousRelease: st.InstalledRelease,
		InstalledAt:     stamp,
		BackupDir:       backupRoot,
	}
	dirs := r.dirs()
	// Capture every pre-image before changing the first target. Once the
	// complete entry is durable, a later invocation can recover from SIGKILL,
	// power loss or a failed state write without guessing which files changed.
	for _, sf := range staged {
		bf := state.BackupFile{Target: sf.Target}
		if sha, ok, err := fileSHA256(sf.Target); err != nil {
			return entry, err
		} else if ok {
			bf.Existed = true
			bf.SHA256Before = sha
			info, err := os.Lstat(sf.Target)
			if err != nil {
				return entry, fmt.Errorf("stat mode %s: %w", sf.Target, err)
			}
			bf.ModeBefore = formatMode(info.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky))
			ownership, err := r.ownershipManager().Stat(sf.Target)
			if err != nil {
				return entry, fmt.Errorf("stat ownership %s: %w", sf.Target, err)
			}
			uid, gid := ownership.UID, ownership.GID
			bf.UIDBefore, bf.GIDBefore = &uid, &gid
			rel := cleanBackupRel(sf.Target)
			bf.BackupPath = filepath.Join(backupRoot, rel)
			if err := copyFile(sf.Target, bf.BackupPath, 0); err != nil {
				return entry, err
			}
		}
		entry.Files = append(entry.Files, bf)
	}
	var orphanTargets []string
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
			info, err := os.Lstat(target)
			if err != nil {
				return entry, fmt.Errorf("stat orphan mode %s: %w", target, err)
			}
			bf.ModeBefore = formatMode(info.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky))
			ownership, err := r.ownershipManager().Stat(target)
			if err != nil {
				return entry, fmt.Errorf("stat ownership %s: %w", target, err)
			}
			uid, gid := ownership.UID, ownership.GID
			bf.UIDBefore, bf.GIDBefore = &uid, &gid
			if err := copyFile(target, bf.BackupPath, 0); err != nil {
				return entry, err
			}
			entry.Files = append(entry.Files, bf)
			orphanTargets = append(orphanTargets, target)
		}
	}
	if err := state.SaveTransaction(r.Config.StateDir, &state.Transaction{Entry: entry}); err != nil {
		return entry, fmt.Errorf("save transaction journal: %w", err)
	}
	for _, sf := range staged {
		if err := r.installFile(sf.StagePath, sf.Target, sf.Mode, sf.Ownership); err != nil {
			return entry, err
		}
		fmt.Fprintf(r.Out, "INSTALL %s\n", sf.Target)
	}
	for _, target := range orphanTargets {
		if err := os.Remove(target); err != nil {
			return entry, err
		}
		if err := durable.SyncDirectory(filepath.Dir(target)); err != nil {
			return entry, err
		}
		fmt.Fprintf(r.Out, "REMOVE-ORPHAN %s\n", target)
	}
	return entry, nil
}

// recoverInterrupted completes the write-ahead protocol before any new
// action. If state.json already contains the transaction's history entry, the
// apply committed and only a stale journal remains. Otherwise every pre-image
// is restored and every newly introduced target is removed.
func (r *Runner) recoverInterrupted() error {
	transaction, err := state.LoadTransaction(r.Config.StateDir)
	if err != nil {
		return fmt.Errorf("load transaction journal: %w", err)
	}
	if transaction == nil {
		return nil
	}
	st, err := state.Load(r.Config.StateDir)
	if err != nil {
		return fmt.Errorf("load state during transaction recovery: %w", err)
	}
	entry := transaction.Entry
	if len(st.History) != 0 {
		last := st.History[len(st.History)-1]
		if last.ReleaseID == entry.ReleaseID && last.InstalledAt == entry.InstalledAt && last.BackupDir == entry.BackupDir {
			if err := state.RemoveTransaction(r.Config.StateDir); err != nil {
				return err
			}
			fmt.Fprintf(r.Out, "[RECOVERY] removed journal for committed release=%s\n", entry.ReleaseID)
			return nil
		}
	}
	if err := r.restoreEntry(entry); err != nil {
		return fmt.Errorf("recover interrupted release %s: %w", entry.ReleaseID, err)
	}
	if err := state.RemoveTransaction(r.Config.StateDir); err != nil {
		return err
	}
	fmt.Fprintf(r.Out, "[RECOVERY] restored pre-upgrade files for interrupted release=%s\n", entry.ReleaseID)
	return nil
}

func (r *Runner) restoreEntry(entry state.HistoryEntry) error {
	if err := validateEntryBackups(entry); err != nil {
		return err
	}
	for index := len(entry.Files) - 1; index >= 0; index-- {
		bf := entry.Files[index]
		if bf.Existed {
			if bf.UIDBefore == nil || bf.GIDBefore == nil {
				return fmt.Errorf("restore %s: backup has no recorded uid/gid", bf.Target)
			}
			mode, err := recordedBackupMode(bf)
			if err != nil {
				return err
			}
			ownership := platform.Ownership{UID: *bf.UIDBefore, GID: *bf.GIDBefore}
			if err := r.installFile(bf.BackupPath, bf.Target, mode, ownership); err != nil {
				return fmt.Errorf("restore %s: %w", bf.Target, err)
			}
			continue
		}
		if err := os.Remove(bf.Target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", bf.Target, err)
		}
		if err := durable.SyncDirectory(filepath.Dir(bf.Target)); err != nil {
			return err
		}
	}
	return nil
}

// validateEntryBackups preflights the complete restore set. A corrupt later
// backup must be detected before an earlier target is changed, otherwise a
// rollback could itself leave a mixed-version installation.
func validateEntryBackups(entry state.HistoryEntry) error {
	for _, bf := range entry.Files {
		if !bf.Existed {
			continue
		}
		if bf.UIDBefore == nil || bf.GIDBefore == nil {
			return fmt.Errorf("restore %s: backup has no recorded uid/gid", bf.Target)
		}
		if _, err := recordedBackupMode(bf); err != nil {
			return err
		}
		if strings.TrimSpace(bf.BackupPath) == "" || strings.TrimSpace(bf.SHA256Before) == "" {
			return fmt.Errorf("restore %s: backup path or digest is missing", bf.Target)
		}
		sha, ok, err := fileSHA256(bf.BackupPath)
		if err != nil {
			return fmt.Errorf("verify backup for %s: %w", bf.Target, err)
		}
		if !ok || !strings.EqualFold(sha, bf.SHA256Before) {
			return fmt.Errorf("restore %s: backup sha256 mismatch", bf.Target)
		}
	}
	return nil
}

func recordedBackupMode(bf state.BackupFile) (os.FileMode, error) {
	if strings.TrimSpace(bf.ModeBefore) == "" {
		return 0, fmt.Errorf("restore %s: backup has no recorded mode", bf.Target)
	}
	mode, err := parseMode(bf.ModeBefore, "file")
	if err != nil {
		return 0, fmt.Errorf("restore %s: invalid recorded mode %q: %w", bf.Target, bf.ModeBefore, err)
	}
	return mode, nil
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
	n, err := strconv.ParseUint(raw, 8, 12)
	if err != nil || n > 0o7777 {
		return 0, fmt.Errorf("bad mode %q", raw)
	}
	mode := os.FileMode(n & 0o777)
	if n&0o4000 != 0 {
		mode |= os.ModeSetuid
	}
	if n&0o2000 != 0 {
		mode |= os.ModeSetgid
	}
	if n&0o1000 != 0 {
		mode |= os.ModeSticky
	}
	return mode, nil
}

func formatMode(mode os.FileMode) string {
	n := uint32(mode.Perm())
	if mode&os.ModeSetuid != 0 {
		n |= 0o4000
	}
	if mode&os.ModeSetgid != 0 {
		n |= 0o2000
	}
	if mode&os.ModeSticky != 0 {
		n |= 0o1000
	}
	return fmt.Sprintf("%04o", n)
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

// installFile publishes a fully prepared inode. Ownership is applied before
// the final chmod because chown clears set-id bits on Unix; doing it in the
// opposite order would silently turn 4511/4550 boundaries into ordinary
// executables. The file and containing directory are synced around the atomic
// rename so a reported successful upgrade has a durable payload.
func (r *Runner) installFile(src, dst string, mode os.FileMode, ownership platform.Ownership) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := tempInstallPath(dst)
	if err := copyFile(src, tmp, mode&os.ModePerm); err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err := r.ownershipManager().Apply(tmp, ownership); err != nil {
		return fmt.Errorf("set uid/gid on %s: %w", tmp, err)
	}
	if mode == 0 {
		st, err := os.Stat(src)
		if err != nil {
			return err
		}
		mode = st.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return fmt.Errorf("set mode on %s: %w", tmp, err)
	}
	if err := syncInstalledFile(tmp); err != nil {
		return err
	}
	if err := verifyInstalledMode(tmp, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	if err := verifyInstalledMode(dst, mode); err != nil {
		return err
	}
	return durable.SyncDirectory(filepath.Dir(dst))
}

func verifyInstalledMode(path string, want os.FileMode) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	mask := os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky
	got := info.Mode() & mask
	want &= mask
	if got != want {
		return fmt.Errorf("verify mode on %s: got %s want %s", path, formatMode(got), formatMode(want))
	}
	return nil
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
