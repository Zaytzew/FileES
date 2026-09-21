//go:build linux || windows

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"filees/internal/durable"
	"filees/pkg/config"
)

func cmdUpdateChannel(args []string) int {
	if err := runUpdateChannel(args, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "update-channel:", err)
		return 1
	}
	return 0
}

func runUpdateChannel(args []string, out io.Writer) error {
	path, rest := parseConfigFlag(args)
	if len(rest) > 1 || (len(rest) == 1 && rest[0] != "alpha" && rest[0] != "beta" && rest[0] != "stable") {
		return errors.New("usage: filees update-channel [alpha|beta|stable] --config path")
	}
	allowed, err := clientChannelSelectionAllowed()
	if err != nil {
		return err
	}
	if !allowed {
		return errors.New("updates are managed by Microsoft Store; release-channel selection is unavailable")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return fmt.Errorf("read existing application configuration: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("configuration must be a regular file, not a symlink")
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return err
	}
	view, err := config.LoadClientView(abs)
	if err != nil {
		return err
	}
	update := view.Update
	if update == nil && !view.UpdateConfigured {
		update, err = distributionClientUpdateConfig()
		if err != nil {
			return err
		}
	}
	if len(rest) == 0 {
		channel := "disabled"
		if update != nil {
			channel = update.Channel
		}
		fmt.Fprintf(out, "configured_channel: %s\nconfig: %s\n", channel, abs)
		return nil
	}
	next, err := config.WithUpdateChannel(data, rest[0], update)
	if err != nil {
		return err
	}
	if !bytes.Equal(data, next) {
		if err := replaceChannelConfig(abs, data, next, info.Mode().Perm()); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "configured_channel: %s\nconfig: %s\nRestart FileES to use this channel. No release was downloaded or installed.\n", rest[0], abs)
	return nil
}

func replaceChannelConfig(path string, previous, next []byte, mode os.FileMode) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".filees-channel-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	if err := temp.Chmod(mode); err != nil {
		return err
	}
	if _, err := temp.Write(next); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	// Do not knowingly replace a configuration edited while we prepared ours.
	current, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, previous) {
		return errors.New("configuration changed; retry channel selection")
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		return err
	}
	return durable.SyncDirectory(filepath.Dir(path))
}
