package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"filees/internal/nativeruntime"
)

// A release process pins its own embedded runtime, including after its EXE is
// renamed by the updater. Environment overrides remain explicit test/developer
// choices; missing payloads never silently select CLI in a bundled build.
var bundledNativeSVN = sync.OnceValues(func() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return nativeruntime.Ensure(filepath.Join(cache, "FileES", "native-svn"), nativeruntime.Payload)
})

func prepareNativeSVN() error {
	if runtime.GOOS != "windows" || len(nativeruntime.Payload) == 0 || os.Getenv("FILEES_NATIVE_SVN") != "" {
		return nil
	}
	_, err := bundledNativeSVN()
	return err
}

// Linux retains record-move only. Windows bundle builds use all native WC/RA
// operations by default; untagged developer builds retain explicit opt-in.
func nativeSVNPath() string {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		return ""
	}
	if path := os.Getenv("FILEES_NATIVE_SVN"); path != "" {
		return path
	}
	if runtime.GOOS == "windows" && len(nativeruntime.Payload) != 0 {
		path, err := bundledNativeSVN()
		if err != nil {
			// main reports this before constructing clients. A caller bypassing
			// startup must fail closed too, never create a CLI-backed client.
			panic(fmt.Sprintf("native SVN runtime unavailable: %v", err))
		}
		return path
	}
	return ""
}
