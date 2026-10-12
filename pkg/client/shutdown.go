package client

import (
	"errors"
	"os/exec"
	"runtime"
)

// IsShutdownInterruption recognizes Windows refusing to initialize a native
// process while its window station is shutting down. It is not a successful
// inspection and must never be used as proof of a mutation's result.
func IsShutdownInterruption(err error) bool {
	var native *NativeFailure
	var exit *exec.ExitError
	return runtime.GOOS == "windows" && errors.As(err, &native) &&
		len(native.Entries) == 0 && !native.Truncated &&
		errors.As(native.Exit, &exit) && uint32(exit.ExitCode()) == 0xc000026b
}
