//go:build !windows

package whaleclient

func (m *Manager) systemSpoolCandidates(sourcePath string) ([]spoolCandidate, error) {
	available, err := filesystemAvailable(m.Root)
	if err != nil {
		return nil, err
	}
	return []spoolCandidate{{Root: m.Root, VolumeID: m.Root, DeviceID: m.Root, Available: available}}, nil
}

func systemPathIdentity(path string) (root, volumeID, deviceID string, err error) {
	return "", "", "", nil
}
