//go:build !windows && !linux && !freebsd && !openbsd && !netbsd

package trash

import "errors"

func move(string) error {
	return errors.New("recycle bin is not supported on this system; nothing was removed")
}
