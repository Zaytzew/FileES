package mobileworker

import "io"

// Copy at most limit bytes to disk. Probe one additional byte only in memory,
// so malformed input cannot write even one byte above the declared boundary.
// Callers still verify the exact size/hash before accepting the payload.
func copyUploadBytes(dst io.Writer, src io.Reader, limit int64) (int64, error) {
	if limit < 0 {
		return 0, errUploadLimit
	}
	n, err := io.Copy(dst, io.LimitReader(src, limit))
	if err != nil {
		return n, err
	}
	var extra [1]byte
	read, err := io.ReadFull(src, extra[:])
	if read != 0 {
		return n, errUploadLimit
	}
	if err != io.EOF {
		return n, err
	}
	return n, nil
}
