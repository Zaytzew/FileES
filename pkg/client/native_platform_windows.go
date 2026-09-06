//go:build windows

package client

func nativeWCOps(c *execClient) bool { return c.nativeSVNPath != "" }
