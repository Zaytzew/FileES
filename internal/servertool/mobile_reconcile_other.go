//go:build !linux && !openbsd

package servertool

import (
	"fmt"
	"io"
)

func runAdminMobileRecover(_ string, _ []string, _ io.Writer, stderr io.Writer) int {
	fmt.Fprintln(stderr, "mobile recovery is supported on Linux and OpenBSD servers")
	return ExitUsage
}
