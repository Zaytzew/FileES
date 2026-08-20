package whaleclient

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"filees/pkg/privatefile"
)

type spoolCandidate struct {
	Root      string
	VolumeID  string
	DeviceID  string
	Available int64
}

type spoolSelection struct {
	Root          string
	VolumeID      string
	DeviceID      string
	ReservedBytes int64
}

// EnableSystemSpoolSelection makes every new PUT choose its spool from the
// fixed local volumes visible at BEGIN time. The selected volume is persisted
// in the operation; recovery never runs the selection again.
func (m *Manager) EnableSystemSpoolSelection() {
	m.SpoolCandidates = m.systemSpoolCandidates
}

func (m *Manager) selectSpool(sourcePath string, contentBytes int64) (spoolSelection, error) {
	provider := m.SpoolCandidates
	if provider == nil {
		available, err := filesystemAvailable(m.Root)
		if err != nil {
			return spoolSelection{}, err
		}
		provider = func(string) ([]spoolCandidate, error) {
			return []spoolCandidate{{Root: m.Root, VolumeID: filepath.Clean(m.Root), DeviceID: filepath.Clean(m.Root), Available: available}}, nil
		}
	}
	candidates, err := provider(sourcePath)
	if err != nil {
		return spoolSelection{}, err
	}
	if len(candidates) == 0 {
		return spoolSelection{}, errors.New("no eligible local volume is available for Whale spool")
	}

	type ranked struct {
		spoolCandidate
		remaining int64
		distinct  bool
	}
	sourceDevice := ""
	identity := m.SpoolSourceIdentity
	if identity == nil {
		identity = systemPathIdentity
	}
	if _, _, deviceID, identityErr := identity(sourcePath); identityErr != nil {
		return spoolSelection{}, fmt.Errorf("identify Whale source volume: %w", identityErr)
	} else {
		sourceDevice = deviceID
	}
	var eligible []ranked
	for _, candidate := range candidates {
		candidate.Root = filepath.Clean(candidate.Root)
		if !filepath.IsAbs(candidate.Root) || strings.TrimSpace(candidate.VolumeID) == "" || strings.TrimSpace(candidate.DeviceID) == "" {
			continue
		}
		reserved, reservationErr := m.outstandingReservations(candidate.VolumeID)
		if reservationErr != nil {
			return spoolSelection{}, fmt.Errorf("read Whale spool reservations: %w", reservationErr)
		}
		available := candidate.Available - reserved
		margin := spaceSafetyMargin(contentBytes)
		if contentBytes > available-margin {
			continue
		}
		eligible = append(eligible, ranked{
			spoolCandidate: candidate,
			remaining:      available - contentBytes,
			distinct:       sourceDevice != "" && candidate.DeviceID != sourceDevice,
		})
	}
	if len(eligible) == 0 {
		return spoolSelection{}, fmt.Errorf("insufficient space for Whale spool on eligible local volumes: required=%d safety=%d", contentBytes, spaceSafetyMargin(contentBytes))
	}
	sort.Slice(eligible, func(i, j int) bool {
		if eligible[i].distinct != eligible[j].distinct {
			return eligible[i].distinct
		}
		if eligible[i].remaining != eligible[j].remaining {
			return eligible[i].remaining > eligible[j].remaining
		}
		return eligible[i].VolumeID < eligible[j].VolumeID
	})
	for _, candidate := range eligible {
		if err := privatefile.EnsureDir(candidate.Root); err != nil {
			continue
		}
		return spoolSelection{Root: candidate.Root, VolumeID: candidate.VolumeID, DeviceID: candidate.DeviceID, ReservedBytes: contentBytes}, nil
	}
	return spoolSelection{}, errors.New("eligible Whale spool volumes are not writable")
}

func (m *Manager) outstandingReservations(volumeID string) (int64, error) {
	operations, err := m.List()
	if err != nil {
		return 0, err
	}
	var total int64
	for _, op := range operations {
		if op.Direction != "put" || op.SpoolVolumeID != volumeID || op.ReservedBytes <= 0 || terminalOperation(op.State) {
			continue
		}
		occupied := spoolAllocatedBytes(m.spoolOperationDir(op))
		if occupied < op.ReservedBytes {
			total += op.ReservedBytes - occupied
		}
	}
	return total, nil
}

func spoolAllocatedBytes(dir string) int64 {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var total int64
	for _, entry := range entries {
		if entry.IsDir() || (entry.Name() != "payload.ready" && !strings.HasPrefix(entry.Name(), ".capture-")) {
			continue
		}
		if info, err := entry.Info(); err == nil && info.Mode().IsRegular() {
			total += info.Size()
		}
	}
	return total
}

func terminalOperation(state OperationState) bool {
	return state == StatePublished || state == StateLocal || state == StateCancelled
}
