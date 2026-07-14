// Package platform defines the operating-system boundary used by the GUI.
// It contains no platform implementation and no daemon/IPC concepts.
package platform

import "context"

// Backend is the complete set of operating-system services required by the
// tray application. Consumers should depend on the smaller embedded interfaces
// whenever they need only one capability.
type Backend interface {
	FolderOpener
	FilePicker
	Notifier
	Autostart
}

type FolderOpener interface {
	OpenFolder(ctx context.Context, path string) error
}

type FilePicker interface {
	PickFiles(ctx context.Context, request PickFilesRequest) (PickFilesResult, error)
}

type Notifier interface {
	Notify(ctx context.Context, notification Notification) error
}

type Autostart interface {
	AutostartStatus(ctx context.Context, spec AutostartSpec) (AutostartState, error)
	SetAutostart(ctx context.Context, spec AutostartSpec, enabled bool) error
}

// PickFilesRequest describes a native file picker without prescribing its UI
// toolkit. Root is the repository boundary presented to the user; callers must
// still validate the returned absolute paths before a daemon operation.
type PickFilesRequest struct {
	Title         string
	Root          string
	InitialDir    string
	AllowMultiple bool
}

// PickFilesResult treats user cancellation as a normal outcome, not an error.
type PickFilesResult struct {
	Paths     []string
	Cancelled bool
}

// Notification is intentionally informational. It contains no callback or
// mutating action, so a platform adapter cannot bypass the GUI action boundary.
type Notification struct {
	ID      string
	Group   string
	Title   string
	Body    string
	Urgency Urgency
}

type Urgency string

const (
	UrgencyLow      Urgency = "low"
	UrgencyNormal   Urgency = "normal"
	UrgencyCritical Urgency = "critical"
)

// AutostartSpec identifies the per-user application entry. Executable must be
// absolute before it reaches a native adapter.
type AutostartSpec struct {
	ID         string
	Name       string
	Executable string
	Args       []string
}

type AutostartState struct {
	Enabled bool
	Current bool   // enabled entry launches the executable and arguments from the supplied spec
	Source  string // adapter-specific diagnostic label, never interpreted by app
}
