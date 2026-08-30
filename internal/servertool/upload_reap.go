package servertool

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"filees/internal/uploadworker"
	"filees/pkg/avscan"
	"filees/pkg/serverconfig"
	"filees/public-shares/channel"
	"filees/public-shares/intake"
)

func RunUploadReap(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	path, rest, err := configPath(args)
	if err != nil || len(rest) != 0 {
		report(stderr, "upload-reap arguments", err)
		return ExitUsage
	}
	config, err := serverconfig.LoadFor(path, serverconfig.SecretActivation|serverconfig.SecretOTP)
	if err != nil {
		report(stderr, "upload-reap config", err)
		return ExitConfig
	}
	if !config.Upload.Enabled() {
		fmt.Fprintln(stdout, "upload intake is not configured")
		return ExitOK
	}
	if len(config.Upload.AVCommand) == 0 {
		fmt.Fprintln(stderr, "upload-reap: av_command is required")
		return ExitConfig
	}
	r := config.Repositories
	stateRoot := config.PublicShares.EffectiveStateRoot(r.ResultsRoot)
	reaper := uploadworker.Reaper{
		Intake:    intake.Store{Root: config.Upload.IntakeRoot},
		Channels:  &channel.Store{Root: stateRoot, TokenKey: config.Onboarding.OTPPepper},
		ReposRoot: r.Root,
		TrashRoot: config.Upload.EffectiveTrashRoot(r.ResultsRoot),
		Scanner:   avscan.Command{Path: config.Upload.AVCommand[0], Args: config.Upload.AVCommand[1:]},
		Publisher: uploadworker.Publisher{SVNMucc: r.EffectiveSVNMuccBinary(), SVNLook: r.EffectiveSVNLookBinary()},
	}
	if err := os.MkdirAll(reaper.TrashRoot, 0700); err != nil {
		report(stderr, "upload-reap trash", err)
		return ExitConfig
	}
	summary, err := reaper.Reap(context.Background())
	if err != nil {
		report(stderr, "upload-reap", err)
		return ExitSoftware
	}
	fmt.Fprintf(stdout, "accepted=%d rejected=%d failed=%d\n", summary.Accepted, summary.Rejected, summary.Failed)
	return ExitOK
}

func RunUploadSeedReject(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	path, rest, err := configPath(args)
	if err != nil {
		report(stderr, "upload-seed-reject arguments", err)
		return ExitUsage
	}
	alias, name := "", "eicar.com"
	for i := 0; i < len(rest); {
		if i+1 >= len(rest) {
			fmt.Fprintln(stderr, "upload-seed-reject: -alias is required")
			return ExitUsage
		}
		switch rest[i] {
		case "-alias":
			alias, rest = rest[i+1], rest[i+2:]
		case "-name":
			name, rest = rest[i+1], rest[i+2:]
		default:
			fmt.Fprintln(stderr, "upload-seed-reject: unknown argument")
			return ExitUsage
		}
	}
	if strings.TrimSpace(alias) == "" {
		fmt.Fprintln(stderr, "upload-seed-reject: -alias is required")
		return ExitUsage
	}
	config, err := serverconfig.LoadFor(path, serverconfig.SecretActivation|serverconfig.SecretOTP)
	if err != nil {
		report(stderr, "upload-seed-reject config", err)
		return ExitConfig
	}
	if !config.Upload.Enabled() {
		fmt.Fprintln(stderr, "upload-seed-reject: upload intake is not configured")
		return ExitConfig
	}
	stateRoot := config.PublicShares.EffectiveStateRoot(config.Repositories.ResultsRoot)
	store := &channel.Store{Root: stateRoot, TokenKey: config.Onboarding.OTPPepper}
	owner, err := store.OwnerRealmForAlias(alias)
	if err != nil {
		report(stderr, "upload-seed-reject alias", err)
		return ExitData
	}
	reaper := uploadworker.Reaper{TrashRoot: config.Upload.EffectiveTrashRoot(config.Repositories.ResultsRoot)}
	if err := os.MkdirAll(reaper.TrashRoot, 0700); err != nil {
		report(stderr, "upload-seed-reject trash", err)
		return ExitConfig
	}
	idx, err := reaper.SeedReject(owner, name, time.Time{})
	if err != nil {
		report(stderr, "upload-seed-reject", err)
		return ExitSoftware
	}
	fmt.Fprintf(stdout, "upload_id=%s name=%s remaining_hours=%d\n", idx.UploadID, idx.OriginalName, remainingHoursForSeed(idx))
	return ExitOK
}

func remainingHoursForSeed(idx uploadworker.Index) int {
	hours := int((idx.Remaining(time.Now().UTC()) + time.Hour - 1) / time.Hour)
	if hours < 1 {
		return 1
	}
	return hours
}
