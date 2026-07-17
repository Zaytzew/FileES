package main

import (
	"fmt"
	"io"

	"filees/internal/serverinstall/config"
)

func runCheckConfig(cfg *config.Config, out io.Writer) {
	fmt.Fprintf(out, "config:     %s\n", cfg.ConfigPath)
	fmt.Fprintf(out, "repo.url:   %s\n", cfg.RepoURL)
	fmt.Fprintf(out, "channel:    %s\n", cfg.Channel)
	fmt.Fprintf(out, "platform:   %s\n", cfg.Platform)
	fmt.Fprintf(out, "svn:        %s\n", cfg.SVNPath)
	fmt.Fprintf(out, "state_dir:  %s\n", cfg.StateDir)
	fmt.Fprintf(out, "stage_dir:  %s\n", cfg.StageDir)
	fmt.Fprintf(out, "backup_dir: %s\n", cfg.BackupDir)
	fmt.Fprintf(out, "sbin_dir:   %s\n", cfg.SbinDir)
	fmt.Fprintf(out, "sysconf:    %s\n", cfg.SysconfDir)
	fmt.Fprintf(out, "sshd_conf:  %s\n", cfg.SSHDConfDir)
	fmt.Fprintf(out, "data_dir:   %s\n", cfg.DataDir)
	fmt.Fprintf(out, "action:     %s\n", cfg.DefaultAction)
	fmt.Fprintf(out, "drift:      %s\n", cfg.ConfigDrift)
	fmt.Fprintf(out, "orphans:    %s\n", cfg.OrphanFiles)
	fmt.Fprintf(out, "require_hash:       %v\n", cfg.RequireHash)
	fmt.Fprintf(out, "verify_signature:   %v\n", cfg.VerifySignature)
	if cfg.VerifySignature {
		configured := pubkeyConfigured()
		fmt.Fprintf(out, "release_key: embedded (configured=%v)\n", configured)
	} else {
		fmt.Fprintln(out, "release_key: disabled")
	}
}
