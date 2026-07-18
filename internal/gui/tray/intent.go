package tray

// IntentKind identifies a user action emitted by the tray. Platform and app
// composition layers decide how to execute it.
type IntentKind string

const (
	IntentOpenFolder IntentKind = "open_folder"
	IntentLock       IntentKind = "lock"
	IntentUnlock     IntentKind = "unlock"
	IntentReconnect  IntentKind = "reconnect"
	IntentActivate   IntentKind = "activate"
	IntentQuit       IntentKind = "quit"
)

// Intent contains no engine object and is safe to pass across the GUI boundary.
type Intent struct {
	Kind   IntentKind
	RepoID string
}
