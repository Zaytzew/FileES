//go:build !windows && !nocfapi

package main

import "context"

func (m *anchorManager) reconcileShell(context.Context) bool { return true }
