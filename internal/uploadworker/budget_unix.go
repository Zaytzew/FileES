//go:build !windows

package uploadworker

import (
	"filees/public-shares/storage"
	"io"
)

func ownTrashRoot(root string) (io.Closer, error) { return storage.Own(root) }
