package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"filees/internal/gui/platform"
)

// predecessorNames are product names, deliberately not translated: they are
// what the user sees in Start and in the Store.
var predecessorNames = map[string]string{
	"msi":   "FileES (MSI)",
	"store": "FileES (Microsoft Store)",
}

// runReplacePredecessorWindow asks whether to remove the other FileES variant
// and, if so, runs `filees replace-predecessor`. It is started by the Store
// launcher or the MSI supervisor before either starts a daemon, so like the
// initial channel choice it is a standalone prompt with no IPC. The exit status
// is the answer the caller acts on: success only when the predecessor is gone.
func runReplacePredecessorWindow(from, configPath string) error {
	if _, ok := predecessorNames[from]; !ok {
		return fmt.Errorf("unknown FileES variant %q", from)
	}
	if !filepath.IsAbs(configPath) {
		return errors.New("--config must be an absolute path")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	name := "filees"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	daemon := filepath.Join(filepath.Dir(executable), name)
	return runStandalonePrompt("filees-predecessor", func(prompts *PromptService) error {
		return replacePredecessor(context.Background(), prompts, from, func(settings string) error {
			// Stopping the other daemon may take its time (it finishes a scan
			// and writes its manifest) and an MSI uninstall is not instant.
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, daemon, "replace-predecessor", "--from", from, "--settings", settings, "--config", configPath)
			prepareChannelCommand(cmd)
			output, err := cmd.CombinedOutput()
			if err != nil {
				return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
			}
			return nil
		})
	})
}

// replacePredecessor keeps the owner's rule in one place: the question offers
// keeping the previous version's settings, keeping them is the default, and
// nothing is removed unless the user confirms.
func replacePredecessor(ctx context.Context, prompts initialChannelPrompter, from string, replace func(settings string) error) error {
	previous, ok := predecessorNames[from]
	if !ok {
		return fmt.Errorf("unknown FileES variant %q", from)
	}
	choice, err := prompts.SelectOne(ctx, PromptSelectRequest{
		PresentationKey: "select.replacePredecessor", PresentationArgs: map[string]string{"previous": previous},
		Title: "FileES", Default: "keep",
		Options: []PromptOption{{Value: "keep", Label: "Keep settings"}, {Value: "default", Label: "Do not carry settings"}},
	})
	if err != nil {
		return err
	}
	if choice.Cancelled {
		return errors.New("replacing the previous version was cancelled; application was not started")
	}
	if choice.Value != "keep" && choice.Value != "default" {
		return errors.New("unsupported settings choice")
	}
	if err := replace(choice.Value); err != nil {
		_ = prompts.ShowInfo(ctx, platform.InfoRequest{
			PresentationKey:  "info.replacePredecessorFailed",
			PresentationArgs: map[string]string{"reason": err.Error()},
		})
		return err
	}
	return nil
}
