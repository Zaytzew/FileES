//go:build !openbsd && !linux

package platform

import "fmt"

func New() (Backend, error) {
	return nil, fmt.Errorf("filees-install is not supported on this platform")
}
