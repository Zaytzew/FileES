package servertool

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"filees/internal/obsandbox"
	"filees/pkg/activation"
	"filees/pkg/onboarding"
	"filees/pkg/serverconfig"
)

const (
	ExitOK          = 0
	ExitUsage       = 64
	ExitData        = 65
	ExitUnavailable = 69
	ExitSoftware    = 70
	ExitTempFail    = 75
	ExitConfig      = 78
)

const (
	readPromises    = "stdio rpath wpath flock"
	writePromises   = "stdio rpath wpath cpath fattr flock"
	mailPromises    = writePromises + " inet dns"
	workerPromises  = writePromises + " inet proc exec"
	svnPromises     = writePromises + " proc exec"
	svnExecPromises = "stdio rpath wpath cpath fattr flock proc prot_exec unveil"
	// Whale is a long-lived, stateful repository worker. It needs proc/exec to
	// launch svnlook/svnmucc and must retain prot_exec so the native OpenBSD SVN
	// binaries can establish their own W^X mappings after exec.
	whaleWorkerPromises = writePromises + " proc exec prot_exec"
	whaleExecPromises   = whaleWorkerPromises + " unveil"
)

type toolAccess struct {
	name               string
	areas              onboarding.Area
	write              bool
	needOTP            bool
	needSMTP           bool
	needWorker         bool
	needWorkerPublic   bool
	needActivation     bool
	needRepoResults    bool
	needRepositoryData bool
	needRepoInspection bool
	// needRotationArchive unveils the directory a rotated generation is frozen
	// into. Only repo rotate asks for it: no other command may write there.
	needRotationArchive bool
	needSVN             bool
	needRealmAlias      bool
	repositoryRoot      string
	repositoryAuthz     string
	svnAdminBinary      string
	svnLookBinary       string
	rotationArchiveRoot string
}

func (access toolAccess) promises() string {
	if access.needSMTP {
		return mailPromises
	}
	if access.needWorker {
		return workerPromises
	}
	if access.needSVN {
		return svnPromises
	}
	if access.write {
		return writePromises
	}
	return readPromises
}

func openFiles(configPath string, access toolAccess) (*onboarding.Files, serverconfig.Config, error) {
	if err := sandboxBegin(access.promises()); err != nil {
		return nil, serverconfig.Config{}, err
	}
	var secrets serverconfig.Secrets
	if access.needOTP {
		secrets |= serverconfig.SecretOTP
	}
	if access.needSMTP {
		secrets |= serverconfig.SecretSMTP
	}
	if access.needWorker {
		secrets |= serverconfig.SecretWorker
	}
	if access.needWorkerPublic {
		secrets |= serverconfig.SecretWorkerPublic
	}
	if access.needActivation || access.needSVN {
		secrets |= serverconfig.SecretActivation
	}
	config, err := serverconfig.LoadFor(configPath, secrets)
	if err != nil {
		return nil, serverconfig.Config{}, err
	}
	if access.needRepositoryData && config.Repositories.ResultsRoot != "" {
		access.repositoryRoot = config.Repositories.Root
		access.repositoryAuthz = config.Repositories.DataAuthzFile
		access.svnAdminBinary = config.Repositories.SVNAdminBinary
		if access.needRepoInspection {
			access.svnLookBinary = config.Repositories.EffectiveSVNLookBinary()
		}
	}
	repositoryAccess := onboarding.Access{Areas: access.areas, NeedOTP: access.needOTP}
	if err := onboarding.CheckExisting(config.Root, repositoryAccess); err != nil {
		return nil, serverconfig.Config{}, err
	}
	publicShareStateRoot := ""
	if config.PublicShares.Enabled {
		publicShareStateRoot = config.PublicShares.EffectiveStateRoot(config.Repositories.ResultsRoot)
	}
	if access.needRotationArchive {
		access.rotationArchiveRoot = config.Repositories.RotationArchiveRoot
	}
	profile := repositoryProfile(config.Root, access, config.Activation, config.Repositories.ResultsRoot, config.Repositories.DeletionArchiveRoot, publicShareStateRoot)
	var sandboxErr error
	if access.needSVN {
		// The OpenBSD ports build of svn establishes its own unveil table after
		// exec.  unveil therefore has to survive in the child promise set; the
		// parent's table still limits which paths the child can reveal.
		sandboxErr = sandboxApplyForExec(profile, svnExecPromises)
	} else {
		sandboxErr = sandboxApply(profile)
	}
	if sandboxErr != nil {
		return nil, serverconfig.Config{}, sandboxErr
	}
	files, err := onboarding.OpenPrepared(config.Root, config.Onboarding, repositoryAccess)
	if err != nil {
		return nil, serverconfig.Config{}, err
	}
	return files, config, nil
}

func repositoryProfile(root string, access toolAccess, activationConfig activation.Config, repositoryResultsRoot, deletionArchiveRoot, publicShareStateRoot string) obsandbox.Profile {
	areaPerms := "r"
	if access.write {
		areaPerms = "rwc"
	}
	paths := []obsandbox.Path{{Label: "lock", Name: filepath.Join(root, ".toolchain.lock"), Perms: "rw"}}
	for _, item := range []struct {
		area       onboarding.Area
		label, dir string
	}{
		{onboarding.AreaTickets, "tickets", "tickets"},
		{onboarding.AreaOperations, "operations", "operations"},
		{onboarding.AreaAudit, "audit", "audit"},
	} {
		if access.areas&item.area != 0 {
			paths = append(paths, obsandbox.Path{Label: item.label, Name: filepath.Join(root, item.dir), Perms: areaPerms})
		}
	}
	if access.needSMTP {
		paths = append(paths,
			obsandbox.Path{Label: "resolver", Name: "/etc/resolv.conf", Perms: "r"},
			obsandbox.Path{Label: "hosts", Name: "/etc/hosts", Perms: "r"},
		)
	}
	if access.needActivation {
		sessionRoot := activationConfig.SessionRoot
		if sessionRoot == "" {
			sessionRoot = filepath.Join(activationConfig.Root, "sessions")
		}
		paths = append(paths, obsandbox.Path{Label: "activation", Name: activationConfig.Root, Perms: "rwc"})
		// Same atomic-write requirement as data-authz below: renderAccessLocked
		// replaces both files by temp-sibling-then-rename. Unveiling only the
		// exact file works while Activation.Root happens to be its parent (the
		// default layout) but silently fails with a misleading ENOENT once
		// either is configured outside that tree.
		if atomicFileParentNeedsOwnUnveil(activationConfig.Root, activationConfig.AuthorizedKeysFile) {
			paths = append(paths, obsandbox.Path{Label: "client-authorized-keys-parent", Name: filepath.Dir(activationConfig.AuthorizedKeysFile), Perms: "rwc"})
		}
		paths = append(paths, obsandbox.Path{Label: "client-authorized-keys", Name: activationConfig.AuthorizedKeysFile, Perms: "rwc"})
		if atomicFileParentNeedsOwnUnveil(activationConfig.Root, activationConfig.AuthzFile) {
			paths = append(paths, obsandbox.Path{Label: "service-authz-parent", Name: filepath.Dir(activationConfig.AuthzFile), Perms: "rwc"})
		}
		paths = append(paths,
			obsandbox.Path{Label: "service-authz", Name: activationConfig.AuthzFile, Perms: "rwc"},
			obsandbox.Path{Label: "session-root", Name: sessionRoot, Perms: "rwc"},
		)
		if activationConfig.DataAuthzFile != "" {
			// Grant-authority publication replaces data_authz_file atomically:
			// it creates a temporary sibling, renames it over the configured
			// file, and fsyncs the containing directory.  Unveiling only the
			// final file works while it is read, but makes the atomic writer
			// fail with a misleading ENOENT (typically "mkdir /var") when the
			// authz file lives outside Activation.Root.
			if atomicFileParentNeedsOwnUnveil(activationConfig.Root, activationConfig.DataAuthzFile) {
				paths = append(paths, obsandbox.Path{Label: "data-authz-parent", Name: filepath.Dir(activationConfig.DataAuthzFile), Perms: "rwc"})
			}
			paths = append(paths, obsandbox.Path{Label: "data-authz", Name: activationConfig.DataAuthzFile, Perms: "rwc"})
		}
	}
	if access.needRepoResults && repositoryResultsRoot != "" {
		paths = append(paths, obsandbox.Path{Label: "repository-results", Name: repositoryResultsRoot, Perms: "rwc"})
		if deletionArchiveNeedsOwnUnveil(repositoryResultsRoot, deletionArchiveRoot) {
			paths = append(paths, obsandbox.Path{Label: "repository-deletion-archive", Name: deletionArchiveRoot, Perms: "rwc"})
		}
		// The rotation archive holds the frozen generation and, inside it, the
		// work directory the swap builds - internal/svnrotate puts that there
		// deliberately, because the rename across the two would hit EXDEV.
		if access.rotationArchiveRoot != "" {
			paths = append(paths, obsandbox.Path{Label: "repository-rotation-archive", Name: access.rotationArchiveRoot, Perms: "rwc"})
		}
		if deletionArchiveNeedsOwnUnveil(repositoryResultsRoot, publicShareStateRoot) {
			paths = append(paths, obsandbox.Path{Label: "public-share-state", Name: publicShareStateRoot, Perms: "rwc"})
		}
	}
	if access.needRepositoryData && access.repositoryRoot != "" {
		paths = append(paths,
			obsandbox.Path{Label: "repositories-parent", Name: filepath.Dir(access.repositoryRoot), Perms: "r"},
			obsandbox.Path{Label: "repositories", Name: access.repositoryRoot, Perms: "rwc"},
			obsandbox.Path{Label: "svnadmin", Name: access.svnAdminBinary, Perms: "rx"},
		)
		// Same atomic-write requirement as the two data-authz cases above:
		// unveiling only the exact file breaks the temp-sibling-then-rename
		// writer once repositories.data_authz_file is configured outside
		// repositories.root (the tree this profile otherwise unveils).
		if atomicFileParentNeedsOwnUnveil(access.repositoryRoot, access.repositoryAuthz) {
			paths = append(paths, obsandbox.Path{Label: "repository-authz-parent", Name: filepath.Dir(access.repositoryAuthz), Perms: "rwc"})
		}
		paths = append(paths, obsandbox.Path{Label: "repository-authz", Name: access.repositoryAuthz, Perms: "rwc"})
		if access.needRepoInspection && access.svnLookBinary != "" {
			paths = append(paths, obsandbox.Path{Label: "svnlook", Name: access.svnLookBinary, Perms: "rx"})
		}
	}
	if access.needRealmAlias {
		// Read-only, and deliberately narrower than needSVN's full
		// service-working-copy unveil (which also grants svn exec and
		// service-repository access this lookup never needs): just enough
		// for filees-admin's --join-realm-alias to resolve an alias to a
		// realm ID by reading admin/realms/*.json directly, the same
		// records pkg/repoworker/realm_alias.go's Claim/Resolve already
		// read. Missing this unveil doesn't deny with EACCES - unveiled-out
		// paths read back as ENOENT, which repoworker.ResolveAlias then
		// (correctly, from its own vantage point) reports as "alias
		// unavailable" instead of the real cause. Confirmed live: identical
		// code run outside the sandbox as _filees-state resolved the alias
		// correctly on the first try.
		paths = append(paths, obsandbox.Path{Label: "service-working-copy-realms", Name: filepath.Join(activationConfig.ServiceWorkingCopy, "admin", "realms"), Perms: "r"})
	}
	if access.needSVN {
		paths = append(paths,
			// Every service-WC operation is serialized by
			// withServiceWorkingCopy, including read-oriented admin commands.
			// The lock lives in activation.root, outside a custom service-WC
			// tree, so unveiling only the WC otherwise fails with EACCES.
			obsandbox.Path{Label: "service-working-copy-lock", Name: filepath.Join(activationConfig.Root, ".service-wc.lock"), Perms: "rwc"},
			// The ports client canonicalizes and enumerates the parents before
			// entering each configured tree. Reveal names at those two levels,
			// while retaining write/create rights only on the exact WC and repo.
			obsandbox.Path{Label: "service-working-copy-parent", Name: filepath.Dir(activationConfig.ServiceWorkingCopy), Perms: "r"},
			obsandbox.Path{Label: "service-repository-parent", Name: filepath.Dir(activationConfig.ServiceRepository), Perms: "r"},
			obsandbox.Path{Label: "service-working-copy", Name: activationConfig.ServiceWorkingCopy, Perms: "rwc"},
			obsandbox.Path{Label: "service-repository", Name: activationConfig.ServiceRepository, Perms: "rwc"},
			obsandbox.Path{Label: "svn", Name: activationConfig.SVNBinary, Perms: "rx"},
			obsandbox.Path{Label: "null-device", Name: "/dev/null", Perms: "rw"},
			obsandbox.Path{Label: "random", Name: "/dev/urandom", Perms: "r"},
			obsandbox.Path{Label: "loader", Name: "/usr/libexec/ld.so", Perms: "rx"},
			obsandbox.Path{Label: "loader-hints", Name: "/var/run/ld.so.hints", Perms: "r"},
			obsandbox.Path{Label: "system-libraries", Name: "/usr/lib", Perms: "r"},
			obsandbox.Path{Label: "local-libraries", Name: "/usr/local/lib", Perms: "r"},
			obsandbox.Path{Label: "svn-system-config", Name: "/etc/subversion", Perms: "r"},
		)
	}
	return obsandbox.Profile{Name: access.name, Promises: access.promises(), Paths: paths}
}

func deletionArchiveNeedsOwnUnveil(resultsRoot, archiveRoot string) bool {
	if archiveRoot == "" {
		return false
	}
	resultsRoot = filepath.Clean(resultsRoot)
	archiveRoot = filepath.Clean(archiveRoot)
	if resultsRoot == "" || resultsRoot == "." {
		return true
	}
	return archiveRoot != resultsRoot && !strings.HasPrefix(archiveRoot, resultsRoot+string(filepath.Separator))
}

func atomicFileParentNeedsOwnUnveil(coveredRoot, path string) bool {
	if path == "" {
		return false
	}
	coveredRoot = filepath.Clean(coveredRoot)
	parent := filepath.Clean(filepath.Dir(path))
	if coveredRoot == "" || coveredRoot == "." {
		return true
	}
	return parent != coveredRoot && !strings.HasPrefix(parent, coveredRoot+string(filepath.Separator))
}

var (
	sandboxBegin         = obsandbox.Begin
	sandboxNarrow        = obsandbox.Narrow
	sandboxApply         = obsandbox.Apply
	sandboxApplyForExec  = obsandbox.ApplyForExec
	sandboxPledgeForExec = obsandbox.PledgeForExec
)

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func report(stderr io.Writer, prefix string, err error) {
	fmt.Fprintf(stderr, "%s: %v\n", prefix, err)
}

func configPath(args []string) (string, []string, error) {
	path := "/etc/filees/server.json"
	if len(args) >= 2 && args[0] == "-config" {
		path, args = args[1], args[2:]
	}
	if !filepath.IsAbs(path) {
		return "", nil, errors.New("-config path must be absolute")
	}
	return path, args, nil
}
