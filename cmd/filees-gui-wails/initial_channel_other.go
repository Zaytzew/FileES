//go:build !windows

package main

import "os/exec"

func prepareChannelCommand(cmd *exec.Cmd) {}
