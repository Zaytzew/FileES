//go:build !linux

package client

import "os/exec"

// Windows C owns descendants through its kill-on-close Job Object.
func configureNativeProcess(command *exec.Cmd) func() { return func() {} }
