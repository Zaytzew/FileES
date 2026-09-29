package svnrotate

// diagnosticTail drains stderr while retaining only its last 4 KiB. Avoid
// allocating the entire tool output merely to trim it when reporting failure.
type diagnosticTail struct{ data []byte }

func (b *diagnosticTail) Write(p []byte) (int, error) {
	const limit = 4 << 10
	n := len(p)
	if len(p) >= limit {
		b.data = append(b.data[:0], p[len(p)-limit:]...)
	} else {
		if excess := len(b.data) + len(p) - limit; excess > 0 {
			copy(b.data, b.data[excess:])
			b.data = b.data[:len(b.data)-excess]
		}
		b.data = append(b.data, p...)
	}
	return n, nil
}

func (b *diagnosticTail) Bytes() []byte { return b.data }
