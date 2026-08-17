package tray

// IntentKind identifies a user action emitted by the tray. Platform and app
// composition layers decide how to execute it.
type IntentKind string

const (
	IntentOpenFolder       IntentKind = "open_folder"
	IntentLock             IntentKind = "lock"
	IntentUnlock           IntentKind = "unlock"
	IntentReconnect        IntentKind = "reconnect"
	IntentActivate         IntentKind = "activate"
	IntentSetRealmAlias    IntentKind = "set_realm_alias"
	IntentServerInfo       IntentKind = "server_info"
	IntentSettings         IntentKind = "settings"
	IntentRecoveries       IntentKind = "recoveries"
	IntentJournal          IntentKind = "journal"
	IntentReservations     IntentKind = "reservations"
	IntentCreateRepository IntentKind = "create_repository"
	IntentPairMobileDevice IntentKind = "pair_mobile_device"
	IntentUpdatePlan       IntentKind = "update_plan"
	IntentUpdateApply      IntentKind = "update_apply"
	IntentDetachRepository IntentKind = "detach_repository"
	IntentDeleteRepository IntentKind = "delete_repository"
	IntentDetachServer     IntentKind = "detach_server"
	IntentRestartFileES    IntentKind = "restart_filees"
	IntentShutdownFileES   IntentKind = "shutdown_filees"
	IntentPublish          IntentKind = "publish"
	IntentAckNotice        IntentKind = "ack_notice"
)

// Intent contains no engine object and is safe to pass across the GUI boundary.
type Intent struct {
	Kind     IntentKind
	NoticeID string
	RepoID   string
	ServerID string
}
