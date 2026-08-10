//go:build !windows

package main

type workingCopyGuard struct{}

func acquireWorkingCopyGuard(string) (workingCopyGuard, error) { return workingCopyGuard{}, nil }
func (workingCopyGuard) Close() error                          { return nil }
