//go:build !linux && !openbsd

package storagewatch

import "os"

func preservePolicyOwner(*os.File, string) error { return nil }

// Windows does not encode ACL privacy in FileMode permission bits. Server
// admission remains unsupported there; format-only tests can inspect policy.
func privatePolicyMode(os.FileMode) bool { return true }
