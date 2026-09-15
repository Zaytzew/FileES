package errcat

const (
	CodeUnknown     Code = "SYNC-0000"
	CodeNet         Code = "NET-4007"
	CodeConnDropped Code = "NET-4008"
	CodeAuth        Code = "AUTH-4102"
	CodeSessionEnd  Code = "AUTH-4103"
	CodeLockHeld    Code = "LOCK-2001"
	CodeLockPath    Code = "LOCK-2002"
	CodeCommitFail  Code = "COMMIT-3100"
	CodeCommitStale Code = "COMMIT-3101"
	CodeCommitNoVCS Code = "COMMIT-3102"
	CodeRecon       Code = "RECON-3002"
	CodePolicyWait  Code = "POLICY-2201"
	CodeWCBusy      Code = "SYNC-2001"

	KeyUnknown                Key = "sync.unknown"
	KeyNetUnreachable         Key = "net.unreachable"
	KeyConnectionDropped      Key = "net.connection_dropped"
	KeyAuthFailed             Key = "auth.failed"
	KeySessionEnded           Key = "auth.session_ended"
	KeyLockHeldByOther        Key = "lock.held_by_other"
	KeyLockOperation          Key = "lock.operation_failed"
	KeyLockInvalidPath        Key = "lock.invalid_path"
	KeyCommitFailed           Key = "commit.failed"
	KeyCommitRecoveryHeld     Key = "commit.recovery_held"
	KeyCommitOutdated         Key = "commit.outdated"
	KeyCommitNoVCS            Key = "commit.not_versioned"
	KeyReconConflict          Key = "recon.conflict"
	KeyPolicyDeferred         Key = "policy.deferred"
	KeyWorkingCopyBusy        Key = "sync.working_copy_busy"
	KeyMobileOpNotOnServer    Key = "mobile.op.not_on_server"
	KeyMobileTreeNotIngested  Key = "mobile.tree.not_ingested"
	KeyMobileTreeNotAPack     Key = "mobile.tree.not_a_pack"
	KeyMobileTreeCorrupt      Key = "mobile.tree.payload_corrupt"
	KeyWhaleFailed            Key = "whale.operation_failed"
	KeyWhalePathBusy          Key = "whale.path_busy"
	KeyWhaleAccessDenied      Key = "whale.access_denied"
	KeyWhaleOffsetConflict    Key = "whale.offset_conflict"
	KeyWhaleDigestMismatch    Key = "whale.digest_mismatch"
	KeyWhaleInsufficientSpace Key = "whale.insufficient_space"
)

var (
	byKey  = map[Key]Spec{}
	byPair = map[string]Spec{}
	all    []Spec
)

func init() { register(specs...) }

func register(list ...Spec) {
	all = append(all, list...)
	for _, spec := range list {
		if _, exists := byKey[spec.Key]; !exists {
			byKey[spec.Key] = spec
		}
		byPair[pairID(spec.Code, spec.Key)] = spec
	}
}

func pairID(code Code, key Key) string { return string(code) + "\x00" + string(key) }

// All returns every registered (Code, Key) pair in registration order.
func All() []Spec { return append([]Spec(nil), all...) }

// ByKey returns the preferred spec for a message key.
func ByKey(key Key) (Spec, bool) {
	spec, ok := byKey[key]
	return spec, ok
}

// ByPair returns the spec for an exact wire pair.
func ByPair(code Code, key Key) (Spec, bool) {
	spec, ok := byPair[pairID(code, key)]
	return spec, ok
}

// detailField is the raw diagnostic string most failures carry. It is
// declared so a renderer knows it may show it, and typed so a language pack
// cannot put it inside a translated sentence.
var detailField = Field{"detail", ParamDiagnostic}

// detailOnly returns a fresh slice per entry. The specs table is read by
// value all over the project, and a shared backing array would let one
// caller's append reach into every other entry that borrowed it.
func detailOnly() []Field { return []Field{detailField} }

var specs = []Spec{
	// Existing lock-release IPC pairs, now covered by the shared dictionary.
	{"LOCK-2201", "lock_release.unavailable", SevError, HintRetry, nil, "Lock release service is unavailable"},
	{"LOCK-2202", "lock_release.inspection_failed", SevError, HintRetry, nil, "Could not inspect the current reservation"},
	{"LOCK-2203", "lock_release.stale", SevError, HintRequireAction, nil, "The reservation changed before the request completed"},
	{"LOCK-2204", "lock_release.request_failed", SevError, HintRetry, detailOnly(), "Could not submit the lock release request"},
	{"LOCK-2205", "lock_release.not_pending", SevError, HintRequireAction, nil, "The lock release request is no longer pending"},
	{"LOCK-2206", "lock_release.decision_failed", SevError, HintRequireAction, detailOnly(), "Could not complete the lock release decision"},
	{CodePassportDenied, KeyPassportDenied, SevWarn, HintRequireAction, nil, "Passport replacement is not authorized"},
	{CodePassportStale, KeyPassportStale, SevWarn, HintRetryLocal, nil, "Observed reservation is no longer current"},
	{CodePassportUncertain, KeyPassportUncertain, SevError, HintRequireAction, nil, "Passport replacement outcome is uncertain; reconcile before continuing"},
	{CodePassportAborted, KeyPassportAborted, SevWarn, HintRetryLocal, nil, "Interrupted passport preparation was durably closed without granting a new lock"},
	{CodePassportUnavailable, KeyPassportUnavailable, SevError, HintRetryBackoff, nil, "Passport replacement service is unavailable"},
	{CodePassportRequestConflict, KeyPassportRequestConflict, SevError, HintRequireAction, nil, "Passport operation is bound to another request"},
	{CodePathOwnerUnavailable, KeyPathOwnerUnavailable, SevWarn, HintRequireAction, nil, "Authoritative path ownership is unavailable"},
	// Runtime classification (errmap.Classify). These are log/journal
	// events, not necessarily IPC responses.
	{CodeNet, KeyNetUnreachable, SevWarn, HintRetryBackoff, nil, "Network unreachable — retrying with backoff"},
	// Deliberately NOT classified as IsNetwork(): a connection that was live
	// and then died mid-operation (SSH keepalive fired) deserves the prompt
	// notice this key exists for, not the sustained-offline grace window
	// that suppresses generic net.unreachable noise on a routine poll.
	{CodeConnDropped, KeyConnectionDropped, SevWarn, HintRetryBackoff, nil, "Connection dropped mid-operation (SSH keepalive timeout)"},
	{CodeAuth, KeyAuthFailed, SevError, HintAdminOnly, nil, "Authentication failed — check credentials"},
	// The server ended this session (activation check failed or the lease
	// was revoked) and said so on the tunnel's stderr instead of just
	// dropping the connection — see session_supervisor_unix.go's
	// FILEES-SESSION-ENDED marker. Deliberately does not name a specific
	// cause: SessionAllowed's own doc comment treats every non-live state as
	// one fail-closed result, and this key must not claim more certainty
	// than the server itself has.
	{CodeSessionEnd, KeySessionEnded, SevWarn, HintRetryLocal, nil, "Server ended this session (authorization check failed or lease revoked)"},
	{CodeLockHeld, KeyLockHeldByOther, SevError, HintRequireAction, []Field{{"path", ParamPath}, {"holder", ParamText}, {"until", ParamTimestamp}}, "File locked by another user"},
	{CodeLockHeld, KeyLockOperation, SevError, HintRequireAction, detailOnly(), "Lock operation failed"},
	{CodeLockPath, KeyLockInvalidPath, SevError, HintRequireAction, nil, "Path is outside the working copy"},
	{CodeCommitStale, KeyCommitOutdated, SevWarn, HintRetryLocal, nil, "Working copy out of date — update required before next commit"},
	{CodeCommitNoVCS, KeyCommitNoVCS, SevWarn, HintRetryLocal, nil, "Path not under version control"},
	{CodeCommitFail, KeyCommitFailed, SevError, HintRetryLocal, detailOnly(), "Commit failed"},
	{CodeCommitFail, KeyCommitRecoveryHeld, SevWarn, HintRequireAction, detailOnly(), "Publication recovery paused — inspect diagnostics; do not remove pending state"},
	{CodeRecon, KeyReconConflict, SevError, HintRequireAction, nil, "Conflict detected during update"},
	{CodePolicyWait, KeyPolicyDeferred, SevWarn, HintRetryLocal, nil, "Editing-policy migration waiting on a clean working copy"},
	{CodeWCBusy, KeyWorkingCopyBusy, SevWarn, HintRetryLocal, nil, "Working copy is busy in another local process"},
	{CodeUnknown, KeyUnknown, SevError, HintRetryLocal, nil, "Unexpected error"},

	// Protocol envelopes built by protoErr (PROTO-0001) or typed PROTO-000n.
	{"PROTO-0003", "proto.unknown_command", SevError, HintNone, nil, "Unknown command"},
	{"PROTO-0004", "proto.missing_repo_id", SevError, HintNone, nil, "Repository id is required"},
	{"PROTO-0005", "proto.repo_not_found", SevError, HintNone, nil, "Repository is not known to this daemon"},
	{"PROTO-0001", "proto.parse_error", SevError, HintNone, nil, "Request could not be parsed"},
	{"PROTO-0001", "proto.invalid_envelope", SevError, HintNone, nil, "Request envelope is invalid"},
	{"PROTO-0001", "proto.invalid_payload", SevError, HintNone, nil, "Request payload is invalid"},
	{"PROTO-0001", "proto.missing_repo_id", SevError, HintNone, nil, "Repository id is required"},
	{"PROTO-0001", "proto.repo_not_found", SevError, HintNone, nil, "Repository is not known to this daemon"},

	// The domain language packs are compiled in and validated by the build,
	// so this is a can't-happen that must still not be a panic in a handler.
	{"MSG-0001", "messages.catalog_unavailable", SevError, HintAdminOnly, nil, "Domain message catalogue could not be loaded"},

	{"RECOVERY-0001", "recovery.unavailable", SevError, HintNone, nil, "Recovery service is not available"},
	{"RECOVERY-1001", "recovery.download_failed", SevError, HintRequireAction, nil, "Recovery download failed"},

	{"REALM-0001", "realm.alias_unavailable", SevError, HintRetry, nil, "Realm alias service is not available"},
	{"REALM-0001", "realm.remove_unavailable", SevError, HintNone, nil, "Realm removal is not available"},
	{"REALM-0002", "realm.not_activated", SevError, HintNone, nil, "Server is not activated"},
	{"REALM-1001", "realm.alias_rejected", SevError, HintRequireAction, nil, "Realm alias was rejected"},
	{"REALM-1001", "realm.remove_begin_failed", SevError, HintRequireAction, nil, "Realm removal could not start"},
	{"REALM-1002", "realm.remove_confirm_failed", SevError, HintRequireAction, nil, "Realm removal confirmation failed"},
	{"REALM-2001", "realm.alias_required", SevError, HintRequireAction, nil, "A stable realm alias is required"},

	{"SERVER-0001", "server.detach_unavailable", SevError, HintNone, nil, "Server detach is not available"},
	{"SERVER-0003", "server.session_timeout_unavailable", SevError, HintRetry, nil, "Session timeout is not available"},
	{"SERVER-1002", "server.session_timeout_invalid", SevError, HintRequireAction, nil, "Session timeout is invalid"},
	{"SERVER-1003", "server.session_timeout_failed", SevError, HintRequireAction, nil, "Session timeout could not be saved"},
	{"SERVER-0002", "server.not_activated", SevError, HintNone, nil, "Server is not activated"},
	{"SERVER-1001", "server.detach_failed", SevError, HintRequireAction, nil, "Server detach failed"},

	{"LOCK-2101", "reservation.list_failed", SevError, HintRetry, []Field{{"repo_id", ParamIdentifier}, detailField}, "Reservation list failed"},
	{"LOCK-2102", "reservation.invalid_path", SevError, HintNone, nil, "Reservation path is invalid"},
	{"LOCK-2103", "reservation.release_failed", SevError, HintRequireAction, detailOnly(), "Reservation release failed"},
	{"LOCK-2104", "reservation.projection_stale", SevWarn, HintRetryBackoff, []Field{{"server_id", ParamIdentifier}, {"repo_id", ParamIdentifier}, detailField}, "Reservation refresh failed; serving last known projection"},
	{"LOCK-2105", "reservation.projection_corrupt", SevError, HintNone, []Field{{"server_id", ParamIdentifier}, detailField}, "Reservation projection artifact is corrupt or unreadable"},
	{"LOCK-2106", "reservation.projection_write_failed", SevWarn, HintNone, []Field{{"server_id", ParamIdentifier}, detailField}, "Reservation projection could not be persisted"},

	{"GRANT-0001", "realm.grants_unavailable", SevError, HintRetry, nil, "Grant service is not available"},
	{"GRANT-1001", "realm.grant_recipients_unavailable", SevError, HintRetry, nil, "Grant recipient list failed"},
	{"GRANT-1002", "realm.grant_rejected", SevError, HintRequireAction, nil, "Grant mutation was rejected"},
	{"GRANT-1003", "realm.visibility_rejected", SevError, HintRequireAction, nil, "Visibility change was rejected"},
	{"GRANT-2001", "realm.grants_forbidden", SevError, HintNone, nil, "Grant action is forbidden"},

	{"BRANDING-0001", "realm.branding_unavailable", SevError, HintRetry, nil, "Public branding is not available"},
	{"BRANDING-1001", "realm.branding_rejected", SevError, HintRequireAction, nil, "Public branding was rejected"},
	{"BRANDING-2001", "realm.branding_forbidden", SevError, HintNone, nil, "Public branding is forbidden"},

	{"POLICY-0001", "repo.editing_policy_unavailable", SevError, HintRetry, nil, "Editing policy service is not available"},
	{"POLICY-2001", "repo.editing_policy_forbidden", SevError, HintNone, nil, "Editing policy change is forbidden"},
	{"POLICY-2002", "repo.editing_policy_failed", SevError, HintRetry, nil, "Editing policy change failed"},

	{"SHARE-0001", "public_share.unavailable", SevError, HintRetry, nil, "Public share service is not available"},
	{"SHARE-0002", "public_share.list_all_unavailable", SevError, HintRetry, nil, "Public share aggregate is not available"},
	{"SHARE-1001", "public_share.list_failed", SevError, HintRetry, nil, "Public share list failed"},
	{"SHARE-1002", "public_share.rejected", SevError, HintRequireAction, nil, "Public share mutation was rejected"},
	{"SHARE-2001", "public_share.forbidden", SevError, HintNone, nil, "Public share action is forbidden"},

	{"UPLOAD-0001", "upload_channel.unavailable", SevError, HintRetry, nil, "Upload channel service is not available"},
	{"UPLOAD-1001", "upload_channel.list_failed", SevError, HintRetry, nil, "Upload channel list failed"},
	{"UPLOAD-1002", "upload_channel.rejected", SevError, HintRequireAction, nil, "Upload channel mutation was rejected"},
	{"UPLOAD-2001", "upload_channel.forbidden", SevError, HintNone, nil, "Upload channel action is forbidden"},

	{"HISTORY-0001", "history.unavailable", SevError, HintRetry, nil, "Repository history is not available on this client"},
	{"HISTORY-1001", "history.read_failed", SevError, HintRetry, nil, "Repository history could not be read"},
	{"HISTORY-2001", "history.forbidden", SevError, HintNone, nil, "Repository history is not allowed for this activation"},
	{"HISTORY-2002", "history.snapshot_unknown", SevError, HintRequireAction, nil, "History snapshot is unknown or no longer valid"},
	{"HISTORY-2003", "history.invalid_request", SevError, HintRequireAction, nil, "History request is invalid"},
	{"HISTORY-2004", "history.path_absent", SevError, HintNone, nil, "Path was not a folder at that revision"},
	{"HISTORY-2005", "history.destination_refused", SevError, HintRequireAction, nil, "History export destination was refused"},
	{"HISTORY-2006", "history.insufficient_space", SevError, HintRequireAction, nil, "Not enough free space for the history export"},
	{"HISTORY-2007", "history.operation_unknown", SevError, HintNone, nil, "History export operation is unknown"},
	{"HISTORY-2008", "history.operation_state", SevError, HintNone, nil, "History export operation is not in a state for that"},

	{"SHOUT-1001", "shout.nothing_to_publish", SevInfo, HintNone, nil, "No pending changes to publish"},
	{"SHOUT-1001", "shout.invalid_comment", SevError, HintRequireAction, nil, "Shout comment is invalid"},
	{"SHOUT-1002", "shout.read_only", SevError, HintNone, nil, "Repository is read-only"},
	{"SHOUT-1003", "shout.publish_failed", SevError, HintRequireAction, detailOnly(), "Shouting commit failed"},
	{"INTENT-1001", "intent.resolution_refused", SevError, HintRequireAction, detailOnly(), "Intent resolution refused; inspect a fresh plan"},
	{"INTENT-1002", "intent.ambiguous", SevWarn, HintRequireAction, detailOnly(), "Ambiguous file changes require an explicit decision"},
	{"SHOUT-1004", "shout.list_failed", SevError, HintRetryLocal, nil, "Notice list failed"},
	{"SHOUT-1005", "shout.ack_failed", SevError, HintRetryLocal, nil, "Notice acknowledgement failed"},

	{"SYSTEM-0001", "system.lifecycle_unavailable", SevError, HintNone, nil, "Process lifecycle is not available"},
	{"SYSTEM-0002", "system.quiescing", SevWarn, HintRetryBackoff, nil, "Daemon is preparing a safe restart; command was not started"},

	{"REPO-0001", "repo.lifecycle_unavailable", SevError, HintRetry, nil, "Repository lifecycle is not available"},
	{"REPO-2001", "repo.create_forbidden", SevError, HintNone, nil, "Repository creation is forbidden"},
	{"REPO-2002", "repo.invalid_local_intent", SevError, HintRequireAction, nil, "Local folder cannot be attached"},
	{"REPO-2003", "repo.already_attached", SevError, HintNone, nil, "Repository is already attached"},
	{"REPO-2004", "repo.not_attachable", SevError, HintRetry, nil, "Repository is not attachable yet"},
	{"REPO-2005", "repo.attachment_approval_failed", SevError, HintRequireAction, nil, "Attachment approval failed"},
	{"REPO-2006", "repo.not_attached", SevError, HintNone, nil, "Repository is not attached"},
	{"REPO-2007", "repo.relocation_failed", SevError, HintRequireAction, nil, "Working-copy relocation failed"},
	{"REPO-2008", "repo.lifecycle_operation_not_found", SevError, HintNone, nil, "Lifecycle operation was not found"},
	{"REPO-2010", "repo.detach_required_forbidden", SevError, HintNone, nil, "Required repository cannot be detached"},
	{"REPO-2010", "repo.locate_failed", SevError, HintRequireAction, detailOnly(), "Moved working copy could not be rebound"},
	{"REPO-2011", "repo.delete_forbidden", SevError, HintNone, nil, "Repository delete is forbidden"},
	{"REPO-2012", "repo.detach_failed", SevError, HintRequireAction, nil, "Detach failed"},
	{"REPO-2013", "repo.local_cleanup_pending", SevError, HintRetry, nil, "Repository was deleted on the server; local working-copy cleanup is pending"},
	{"REPO-2013", "repo.load_dump_forbidden", SevError, HintNone, nil, "Load-dump is forbidden"},
	{"REPO-2014", "repo.load_dump_failed", SevError, HintRequireAction, nil, "Load-dump failed"},
	{"REPO-2015", "repo.lifecycle_repair_forbidden", SevError, HintNone, nil, "Repository lifecycle repair is forbidden for the current durable state"},
	// Variants of a locate failure. The daemon still reports most of them as
	// free text in Details; the GUI classifies that text into these keys
	// instead of choosing a sentence from it, so both languages have the
	// wording and the daemon can start emitting the keys directly without any
	// presentation change. See UNFINISHED_WORK for the emission side.
	{"REPO-2010", "repo.locate_not_working_copy", SevError, HintRequireAction, nil, "Chosen folder is not a Subversion working copy"},
	{"REPO-2010", "repo.locate_other_repository", SevError, HintRequireAction, nil, "Chosen folder belongs to a different repository"},
	{"REPO-2010", "repo.locate_no_identity", SevError, HintRequireAction, nil, "Chosen folder carries no FileES identity for this share"},
	{"REPO-2010", "repo.locate_overlaps", SevError, HintRequireAction, nil, "Chosen folder overlaps a working copy FileES already tracks"},
	{"REPO-2016", "repo.lifecycle_repair_failed", SevError, HintRequireAction, detailOnly(), "Repository lifecycle repair failed"},
	{"REPO-2017", "repo.recovery_dismiss_unavailable", SevError, HintNone, nil, "Recovery archive cannot be dismissed in its current state"},
	{"REPO-2018", "repo.recovery_dismiss_failed", SevError, HintRequireAction, nil, "Recovery archive dismissal failed"},
	{"REPO-2019", "repo.rename_name_unportable", SevError, HintRequireAction, nil, "The proposed name cannot be represented either"},
	{"REPO-2020", "repo.rename_target_exists", SevError, HintRequireAction, nil, "Something already occupies the new name"},
	{"REPO-2021", "repo.rename_blocked_in_use", SevError, HintRetry, nil, "The system refused: the file is held open"},
	{"REPO-3001", "repo.activity_unavailable", SevError, HintNone, nil, "Activity journal is not available"},

	{"MOBILE-0001", "mobile_pairing.unavailable", SevError, HintRetry, nil, "Mobile pairing is not available"},
	{"MOBILE-1001", "mobile_pairing.server_not_activated", SevError, HintNone, nil, "Mobile pairing needs an activated server"},
	{"MOBILE-1002", "mobile_pairing.begin_failed", SevError, HintRetry, nil, "Mobile pairing could not start"},
	{"MOBILE-2001", KeyMobileOpNotOnServer, SevError, HintRequireAction, nil, "Mobile worker exited 70 — applied filees-mobile-v1 does not know this operation"},
	{"MOBILE-2002", KeyMobileTreeNotIngested, SevError, HintRequireAction, nil, "UPLOAD_TREE reached the host but the worker does not ingest zip-on-wire yet"},
	{"MOBILE-2003", KeyMobileTreeNotAPack, SevError, HintNone, nil, "UPLOAD_TREE payload is a repository zip, not a FileES tree pack"},
	{"MOBILE-2004", KeyMobileTreeCorrupt, SevError, HintRetry, nil, "UPLOAD_TREE zip sha256 or size does not match the header"},

	{"WHALE-1001", KeyWhaleFailed, SevError, HintRetryBackoff, detailOnly(), "Whale operation failed"},
	{"WHALE-2001", KeyWhalePathBusy, SevWarn, HintRetryBackoff, []Field{{"queue_position", ParamNumber}}, "Another Whale generation owns this path"},
	{"WHALE-2002", KeyWhaleAccessDenied, SevError, HintNone, nil, "Whale repository access denied"},
	{"WHALE-2003", KeyWhaleOffsetConflict, SevWarn, HintRetryLocal, []Field{{"offset", ParamNumber}}, "Whale resume offset conflicts with durable state"},
	{"WHALE-2004", KeyWhaleDigestMismatch, SevError, HintRequireAction, nil, "Whale payload size or sha256 mismatch"},
	{"WHALE-2005", KeyWhaleInsufficientSpace, SevError, HintRequireAction, []Field{{"available_bytes", ParamBytes}, {"required_bytes", ParamBytes}}, "Whale storage reservation does not fit"},

	{"ACTIVATION-0001", "activation.unavailable", SevError, HintRetry, nil, "Activation service is not available"},
	{"ACTIVATION-1001", "activation.begin_failed", SevError, HintRetry, nil, "Activation could not start"},
	{"ACTIVATION-1002", "activation.finish_failed", SevError, HintRetry, nil, "Activation could not finish"},
	{"ACTIVATION-1003", "activation.pending_failed", SevError, HintRetry, nil, "Pending activation lookup failed"},
	{"ACTIVATION-1004", "activation.resume_failed", SevError, HintRetry, nil, "Activation resume failed"},

	{"UPDATE-0001", "update.unavailable", SevError, HintNone, nil, "Update service is not available"},
	{"UPDATE-1001", "update.status_failed", SevError, HintRetryBackoff, nil, "Update status failed"},
	{"UPDATE-1002", "update.plan_failed", SevError, HintRetryBackoff, nil, "Update plan failed"},
	{"UPDATE-1003", "update.apply_failed", SevError, HintRequireAction, nil, "Update apply failed"},
}
