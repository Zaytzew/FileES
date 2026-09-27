package v1

// Hard capture limits are shared by the sender spool and authoritative worker.
// ZIP overhead is separately bounded; Kotlin uses a smaller batching target.
const (
	MaxUploadBytes   int64 = 2 << 30
	MaxTreeWireBytes int64 = MaxUploadBytes + 16<<20
	MaxTreeFiles           = 5000
)
