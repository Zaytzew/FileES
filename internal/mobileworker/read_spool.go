package mobileworker

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"filees/public-shares/storage"
)

var ErrReadLimit = errors.New("mobile: download exceeds size limit")
var ErrReadStorage = errors.New("mobile: download storage unavailable")

// Admission is advisory, not a reservation against concurrent processes.
// The file is already open, so probe the filesystem actually used by the spool.
type readSpool struct {
	*os.File
	checkSpace func(string, int64) error
}

func (s readSpool) reserve(size int64) error {
	check := s.checkSpace
	if check == nil {
		check = storage.RequireSpace
	}
	if err := check(filepath.Dir(s.Name()), size); err != nil {
		return fmt.Errorf("%w: %v", ErrReadStorage, err)
	}
	return nil
}

// Refuse the whole overlong write before any bytes reach disk. Keep the error
// even if a child-process wrapper replaces it with an exit-status error.
type readBoundWriter struct {
	dst       io.Writer
	remaining int64
	err       error
}

func (w *readBoundWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if int64(len(p)) > w.remaining {
		w.err = ErrReadLimit
		return 0, w.err
	}
	n, err := w.dst.Write(p)
	w.remaining -= int64(n)
	if n != len(p) && err == nil {
		err = io.ErrShortWrite
	}
	w.err = err
	return n, err
}
