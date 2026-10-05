package mobileworker

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const diagnosticLogLimit = 4 << 20
const diagnosticLogFiles = 4 // active errors.log plus three older generations

// appendDiagnosticLog is called with the permanent errors.log.lock held.
// Only diagnostic files rotate; ledger records and operation fences are untouched.
func appendDiagnosticLog(root, line string) error {
	path := filepath.Join(root, "errors.log")
	info, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil && !info.Mode().IsRegular() {
		return errors.New("unsafe diagnostic log")
	}
	if err == nil && info.Size()+int64(len(line)) > diagnosticLogLimit {
		// Migrate an old unbounded log by retaining only its newest complete lines.
		if info.Size() > diagnosticLogLimit {
			if err := trimDiagnosticLog(path); err != nil {
				return err
			}
		}
		for i := diagnosticLogFiles - 1; i >= 1; i-- {
			dst := fmt.Sprintf("%s.%d", path, i)
			src := path
			if i > 1 {
				src = fmt.Sprintf("%s.%d", path, i-1)
			}
			for _, name := range []string{src, dst} {
				if st, e := os.Lstat(name); e == nil && !st.Mode().IsRegular() {
					return errors.New("unsafe diagnostic archive")
				} else if e != nil && !errors.Is(e, os.ErrNotExist) {
					return e
				}
			}
			if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := os.Rename(src, dst); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.WriteString(line)
	return errors.Join(writeErr, f.Close())
}

func trimDiagnosticLog(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(-diagnosticLogLimit, io.SeekEnd); err != nil {
		return err
	}
	tail, err := io.ReadAll(io.LimitReader(f, diagnosticLogLimit))
	if err != nil {
		return err
	}
	if n := bytes.IndexByte(tail, '\n'); n >= 0 {
		tail = tail[n+1:]
	} else {
		tail = nil
	}
	if err := f.Close(); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".errors-log-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(tail); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
