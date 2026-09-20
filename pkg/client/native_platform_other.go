//go:build !windows && !linux

package client

func nativeWCOps(c *execClient) bool { return false }
