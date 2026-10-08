package cloudfiles

// CF_PLACEHOLDER_STATE bits (cfapi.h). PARTIAL describes readiness for the
// application; PARTIALLY_ON_DISK specifically describes missing local bytes.
// A fully downloaded placeholder can still be PARTIAL, for example while
// the provider validates it. Keep this distinction independent of API calls.
const (
	statePlaceholder     = 0x00000001
	stateSyncRoot        = 0x00000002
	statePartial         = 0x00000010
	statePartiallyOnDisk = 0x00000020
	stateInvalid         = 0xFFFFFFFF
)

func contentMissing(value uint32) bool {
	return value != stateInvalid && value&statePlaceholder != 0 && value&statePartiallyOnDisk != 0
}
