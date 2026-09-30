package mobileworker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

var ErrReadLimit = errors.New("mobile: download exceeds size limit")
var ErrReadStorage = errors.New("mobile: download storage unavailable")

// Allocation is deferred until authorization and the repository size check.
type readSpool struct {
	*os.File
	root       string
	maxBytes   int64
	checkSpace func(string, int64) error
	release    func() error
}

func (s *readSpool) reserve(ctx context.Context, size int64) error {
	if s.File != nil || size < 0 || s.maxBytes < 0 {
		return ErrReadStorage
	}
	if err := s.allocate(ctx, size); err != nil {
		return fmt.Errorf("%w: %v", ErrReadStorage, err)
	}
	return nil
}

func (s *readSpool) Close() error {
	if s.File == nil {
		return nil
	}
	err := s.File.Close()
	s.File = nil
	if s.release != nil {
		err = errors.Join(err, s.release())
		s.release = nil
	}
	return err
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
