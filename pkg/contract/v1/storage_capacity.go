package contract

// StorageCapacityStatus is presentation only. Fresh/stale refers to the last
// authenticated filesystem observation, never publication authorization.
type StorageCapacityStatus struct {
	State          string `json:"state"`
	TotalBytes     uint64 `json:"total_bytes,omitempty"`
	AvailableBytes uint64 `json:"available_bytes,omitempty"`
	MeasuredAt     string `json:"measured_at,omitempty"`
	ExpiresAt      string `json:"expires_at,omitempty"` // client-local receipt deadline, not server clock
}
