package servertool

import (
	"errors"

	"filees/internal/storagewatch"
)

var errStateObservationChanged = errors.New("server state changed during observation; reconciliation required")

// Live reads remain mandatory: native SVN and direct svnadmin do not depend
// on FileES writer admission. The stamp additionally prevents an in-flight
// observation from confirming an artifact across a FileES mutation. An old or
// interrupted writer never enables a cache-only ACK, nor is its marker removed
// here. Authority is still serialized by the caller's service-WC lock.
func stateObservationFence(root string) (func() error, error) {
	gate := storagewatch.Gate{Root: root}
	before, _, err := gate.ObservationStamp()
	if err != nil {
		return nil, err
	}
	return func() error {
		after, _, err := gate.ObservationStamp()
		if err != nil {
			return err
		}
		if after != before {
			return errStateObservationChanged
		}
		return nil
	}, nil
}
