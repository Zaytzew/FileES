package servertool

import (
	"context"
	"fmt"
	"io"
	"os"

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
