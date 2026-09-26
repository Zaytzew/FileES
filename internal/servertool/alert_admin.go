package servertool

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"filees/internal/obsandbox"
	"filees/internal/serveralerts"
	"filees/pkg/activation"
	"filees/pkg/serverconfig"
	"github.com/google/uuid"
)

func runAdminAlertPublish(path string, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("alert publish", flag.ContinueOnError)
	flags.SetOutput(stderr)
	realm := flags.String("realm", "", "recipient realm UUID (active desktops only)")
	key := flags.String("key", "", "stable problem key")
	text := flags.String("text", "", "safe single-line message, no diagnostics or secrets")
	severity := flags.String("severity", "warning", "info, warning or error")
	resolved := flags.Bool("resolve", false, "silently resolve an existing incident")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *key == "" || *text == "" {
		return adminUsage(stderr, flags, "--realm UUID --key KEY --text MESSAGE [--severity warning] [--resolve]")
	}
	if _, err := uuid.Parse(*realm); err != nil {
		return adminUsage(stderr, flags, "--realm UUID --key KEY --text MESSAGE")
	}
	if err := sandboxBegin(svnPromises); err != nil {
		report(stderr, "alert sandbox", err)
		return ExitSoftware
	}
	config, err := serverconfig.LoadFor(path, serverconfig.SecretActivation)
	if err != nil {
		report(stderr, "alert config", err)
		return ExitConfig
	}
	// Keep the established activation profile for authz regeneration; append
	// only the local tools this publisher needs. No new network promises.
	profile := repositoryProfile(config.Root, toolAccess{name: "filees-admin/alert", write: true, needActivation: true, needSVN: true}, config.Activation, "", "", "")
	profile.Paths = append(profile.Paths, obsandbox.Path{Label: "alert-svnlook", Name: config.Repositories.EffectiveSVNLookBinary(), Perms: "rx"}, obsandbox.Path{Label: "alert-svnmucc", Name: config.Repositories.EffectiveSVNMuccBinary(), Perms: "rx"}, obsandbox.Path{Label: "alert-temp", Name: os.TempDir(), Perms: "rwc"})
	if err := sandboxApplyForExec(profile, svnExecPromises); err != nil {
		report(stderr, "alert sandbox", err)
		return ExitSoftware
	}
	if _, err := os.Stat(filepath.Join(config.Activation.ServiceWorkingCopy, "admin", "realms", *realm+".json")); err != nil {
		report(stderr, "alert recipient realm", err)
		return ExitUsage
	}
	manager, err := activation.New(config.Activation, nil)
	if err != nil {
		report(stderr, "alert activation", err)
		return ExitConfig
	}
	if err = manager.RefreshAlertAccess(); err != nil {
		report(stderr, "alert access", err)
		return ExitSoftware
	}
	status := "active"
	if *resolved {
		status = "resolved"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	changed, err := (serveralerts.Publisher{Repository: config.Activation.ServiceRepository, SVNLook: config.Repositories.EffectiveSVNLookBinary(), SVNMucc: config.Repositories.EffectiveSVNMuccBinary()}).Publish(ctx, *realm, *key, *severity, status, *text)
	if err != nil {
		report(stderr, "alert publish", err)
		return ExitSoftware
	}
	fmt.Fprintf(stdout, "alert %s (changed=%t)\n", status, changed)
	return ExitOK
}
