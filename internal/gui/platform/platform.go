// Package platform defines the operating-system boundary used by the GUI.
// It contains no platform implementation and no daemon/IPC concepts.
package platform

import "context"

// Backend is the complete set of operating-system services required by the
// tray application. Consumers should depend on the smaller embedded interfaces
// whenever they need only one capability.
type Backend interface {
	FolderOpener
	FolderPicker
	FilePicker
	Prompter
	ReservationBrowser
	Notifier
	Autostart
}

type Prompter interface {
	PromptText(ctx context.Context, request PromptTextRequest) (PromptTextResult, error)
	ShowInfo(ctx context.Context, request InfoRequest) error
	Confirm(ctx context.Context, request ConfirmRequest) (bool, error)
}

// ReservationBrowser renders the native, server-scoped reservation window.
// It receives display-only data and returns an opaque row ID; token fencing and
// all SVN work remain in the GUI action/daemon layers.
type ReservationBrowser interface {
	ShowReservations(ctx context.Context, request ReservationDialogRequest) (ReservationDialogResult, error)
}

type ReservationDialogRequest struct {
	Title string
	Text  string
	Rows  []ReservationDialogRow
}

type ReservationDialogRow struct {
	ID            string
	Server        string
	WorkingCopy   string
	Path          string
	Owner         string
	CreatedAt     string
	ReleaseStatus string
}

type ReservationDialogAction string

const (
	ReservationDialogClose   ReservationDialogAction = "close"
	ReservationDialogRefresh ReservationDialogAction = "refresh"
	ReservationDialogRelease ReservationDialogAction = "release"
)

type ReservationDialogResult struct {
	Action ReservationDialogAction
	RowID  string
}

type InfoRequest struct {
	Title string
	Text  string
}

type ConfirmRequest struct {
	Title       string
	Text        string
	ConfirmText string
	CancelText  string
}

type PromptTextRequest struct {
	Title       string
	Text        string
	Placeholder string
	Secret      bool
}

type PromptTextResult struct {
	Value     string
	Cancelled bool
}

type FolderOpener interface {
	OpenFolder(ctx context.Context, path string) error
}

type FilePicker interface {
	PickFiles(ctx context.Context, request PickFilesRequest) (PickFilesResult, error)
}

type FolderPicker interface {
	PickFolder(ctx context.Context, request PickFolderRequest) (PickFolderResult, error)
}

type PickFolderRequest struct {
	Title      string
	InitialDir string
}

type PickFolderResult struct {
	Path      string
	Cancelled bool
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
