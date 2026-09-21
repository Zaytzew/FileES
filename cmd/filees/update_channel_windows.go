//go:build windows

package main

func clientChannelSelectionAllowed() (bool, error) {
	packaged, err := windowsPackageIdentityPresent()
	if err != nil {
		return false, err
	}
	return windowsClientSelfUpdateAllowed(packaged, injectedClientUpdateMode)
}
