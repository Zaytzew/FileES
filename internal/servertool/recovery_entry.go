package servertool

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"filees/internal/obsandbox"
	"filees/pkg/repoworker"
	"filees/pkg/serverconfig"
	"github.com/google/uuid"
)

const (
	RecoveryCommand   = "filees recovery-v1"
	recoveryEntryPath = "/usr/local/libexec/filees/filees-recovery-entry"
)

func RunRecoveryEntry(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	return runRecoveryEntry("/etc/filees/server.json", args, stdin, stdout, stderr, getenv, time.Now)
}

func runRecoveryEntry(configPath string, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string, now func() time.Time) int {
	if len(args) < 1 {
		return ExitUsage
	}
	if err := sandboxBegin("stdio rpath"); err != nil {
		report(stderr, "filees-recovery-entry sandbox", err)
		return ExitSoftware
	}
	config, err := serverconfig.LoadFor(configPath, 0)
	if err != nil {
		report(stderr, "filees-recovery-entry config", err)
		return ExitConfig
	}
	root := config.Repositories.ResultsRoot
	if !filepath.IsAbs(root) || !filepath.IsAbs(effectiveDeletionArchiveRoot(config)) {
		fmt.Fprintln(stderr, "filees-recovery-entry: repository recovery configuration is incomplete")
		return ExitConfig
	}
	paths := []obsandbox.Path{
		{Label: "server-config", Name: configPath, Perms: "r"},
		{Label: "recovery-state", Name: root, Perms: "r"},
	}
	if deletionArchiveNeedsOwnUnveil(root, effectiveDeletionArchiveRoot(config)) {
		paths = append(paths, obsandbox.Path{Label: "recovery-archives", Name: effectiveDeletionArchiveRoot(config), Perms: "r"})
	}
	if err := sandboxApply(obsandbox.Profile{Name: "filees-recovery-entry/" + args[0], Promises: "stdio rpath", Paths: paths}); err != nil {
		report(stderr, "filees-recovery-entry sandbox", err)
		return ExitSoftware
	}
	keys := repoworker.RecoveryKeyStore{Root: root + "/recovery-keys"}

	switch args[0] {
	case "authorize":
		if len(args) != 3 {
			return ExitUsage
		}
		publicKey := args[1] + " " + args[2]
		record, err := keys.FindByPublicKey(publicKey, now())
		if err != nil {
			return ExitUnavailable
		}
		fmt.Fprintf(stdout, "restrict,command=%q %s\n", recoveryEntryPath+" serve "+record.OperationID, record.PublicKey)
		return ExitOK
	case "serve":
		if len(args) != 2 || getenv("SSH_ORIGINAL_COMMAND") != RecoveryCommand {
			fmt.Fprintln(stderr, "filees-recovery-entry: rejected command")
			return ExitUnavailable
		}
		return serveRecovery(config, args[1], stdin, stdout, stderr, now())
	default:
		return ExitUsage
	}
}

func serveRecovery(config serverconfig.Config, operationID string, stdin io.Reader, stdout, stderr io.Writer, now time.Time) int {
	if _, err := uuid.Parse(operationID); err != nil {
		return ExitUnavailable
	}
	manifestStore := repoworker.RecoveryManifestStore{Root: config.Repositories.ResultsRoot + "/recovery-manifests"}
	manifest, err := manifestStore.Load(operationID)
	if err != nil {
		report(stderr, "filees recovery manifest", err)
		return ExitUnavailable
	}
	line, err := readRecoveryRequest(stdin)
	if err != nil {
		report(stderr, "filees recovery request", err)
		return ExitData
	}
	fields := strings.Fields(line)
	if len(fields) == 2 && fields[0] == "list" && fields[1] == operationID {
		if err := writeJSON(stdout, manifest); err != nil {
			return ExitSoftware
		}
		return ExitOK
	}
	if len(fields) != 3 || fields[0] != "get" || fields[1] != operationID {
		fmt.Fprintln(stderr, "filees recovery: rejected request")
		return ExitUnavailable
	}
	if !now.UTC().Before(manifest.DownloadUntil) {
		fmt.Fprintln(stderr, "filees recovery: download expired")
		return ExitUnavailable
	}
	var archive *repoworker.RecoveryArchive
	for i := range manifest.Archives {
		if manifest.Archives[i].ArchiveID == fields[2] {
			archive = &manifest.Archives[i]
			break
		}
	}
	if archive == nil {
		fmt.Fprintln(stderr, "filees recovery: archive not found")
		return ExitUnavailable
	}
	// Ordinary repository deletion uses the manifest operation directly.
	// Realm removal predates that capability and derives one delete operation
	// per repository; retain the fallback for its multi-archive manifests.
	deleteOperationID := operationID
	file, err := repoworker.OpenDeletionRecoveryArchive(effectiveDeletionArchiveRoot(config), deleteOperationID, *archive)
	if err != nil {
		deleteOperationID = uuid.NewSHA1(uuid.NameSpaceOID, []byte(operationID+":"+archive.RepoID+":delete")).String()
		file, err = repoworker.OpenDeletionRecoveryArchive(effectiveDeletionArchiveRoot(config), deleteOperationID, *archive)
	}
	if err != nil {
		report(stderr, "filees recovery archive", err)
		return ExitData
	}
	defer file.Close()
	if _, err := io.CopyN(stdout, file, archive.Size); err != nil {
		report(stderr, "filees recovery stream", err)
		return ExitSoftware
	}
	return ExitOK
}

func readRecoveryRequest(reader io.Reader) (string, error) {
	buffered := bufio.NewReader(io.LimitReader(reader, 1025))
	line, err := buffered.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if len(line) > 1024 || strings.Contains(strings.TrimSuffix(line, "\n"), "\n") {
		return "", errors.New("request is too long")
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", errors.New("request is empty")
	}
	return line, nil
}

func effectiveDeletionArchiveRoot(config serverconfig.Config) string {
	if config.Repositories.DeletionArchiveRoot != "" {
		return config.Repositories.DeletionArchiveRoot
	}
	return config.Repositories.ResultsRoot + "/deleted-repositories"
}
