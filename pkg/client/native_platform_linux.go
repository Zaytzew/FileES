//go:build linux

package client

func nativeWCOps(c *execClient) bool { return c.nativeSVNPath != "" }
