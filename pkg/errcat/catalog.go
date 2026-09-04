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

var specs = []Spec{
	// Runtime classification (errmap.Classify). These are log/journal
	// events, not necessarily IPC responses.
	{CodeNet, KeyNetUnreachable, SevWarn, HintRetryBackoff, nil, "Network unreachable — retrying with backoff", "Brak połączenia z siecią"},
	// Deliberately NOT classified as IsNetwork(): a connection that was live
	// and then died mid-operation (SSH keepalive fired) deserves the prompt
	// notice this key exists for, not the sustained-offline grace window
	// that suppresses generic net.unreachable noise on a routine poll.
	{CodeConnDropped, KeyConnectionDropped, SevWarn, HintRetryBackoff, nil, "Connection dropped mid-operation (SSH keepalive timeout)", "Połączenie zostało przerwane w trakcie operacji"},
	{CodeAuth, KeyAuthFailed, SevError, HintAdminOnly, nil, "Authentication failed — check credentials", "Uwierzytelnienie nie powiodło się"},
	// The server ended this session (activation check failed or the lease
	// was revoked) and said so on the tunnel's stderr instead of just
	// dropping the connection — see session_supervisor_unix.go's
	// FILEES-SESSION-ENDED marker. Deliberately does not name a specific
	// cause: SessionAllowed's own doc comment treats every non-live state as
	// one fail-closed result, and this key must not claim more certainty
	// than the server itself has.
	{CodeSessionEnd, KeySessionEnded, SevWarn, HintRetryLocal, nil, "Server ended this session (authorization check failed or lease revoked)", "Serwer zakończył tę sesję — spróbuj ponownie za chwilę"},
	{CodeLockHeld, KeyLockHeldByOther, SevError, HintRequireAction, []string{"path", "holder", "until"}, "File locked by another user", "Plik jest w tej chwili wypożyczony przez kogoś innego"},
	{CodeLockHeld, KeyLockOperation, SevError, HintRequireAction, []string{"detail"}, "Lock operation failed", "Daemon nie wykonał operacji na plikach"},
	{CodeLockPath, KeyLockInvalidPath, SevError, HintRequireAction, nil, "Path is outside the working copy", "Wybrana ścieżka nie należy do repozytorium"},
	{CodeCommitStale, KeyCommitOutdated, SevWarn, HintRetryLocal, nil, "Working copy out of date — update required before next commit", "Kopia robocza jest nieaktualna — najpierw pobierz zmiany"},
	{CodeCommitNoVCS, KeyCommitNoVCS, SevWarn, HintRetryLocal, nil, "Path not under version control", "Ścieżka nie jest pod kontrolą wersji"},
	{CodeCommitFail, KeyCommitFailed, SevError, HintRetryLocal, []string{"detail"}, "Commit failed", "Zapis na serwer nie powiódł się"},
	{CodeRecon, KeyReconConflict, SevError, HintRequireAction, nil, "Conflict detected during update", "Wykryto konflikt podczas aktualizacji"},
	{CodePolicyWait, KeyPolicyDeferred, SevWarn, HintRetryLocal, nil, "Editing-policy migration waiting on a clean working copy", "Zmiana polityki blokad czeka na czystą kopię roboczą"},
	{CodeWCBusy, KeyWorkingCopyBusy, SevWarn, HintRetryLocal, nil, "Working copy is busy in another local process", "Kopia robocza jest chwilowo zajęta przez inny lokalny proces"},
	{CodeUnknown, KeyUnknown, SevError, HintRetryLocal, nil, "Unexpected error", "Nieoczekiwany błąd"},

	// Protocol envelopes built by protoErr (PROTO-0001) or typed PROTO-000n.
	{"PROTO-0003", "proto.unknown_command", SevError, HintNone, nil, "Unknown command", "Nieznana komenda"},
	{"PROTO-0004", "proto.missing_repo_id", SevError, HintNone, nil, "Repository id is required", "Brak identyfikatora repozytorium"},
	{"PROTO-0005", "proto.repo_not_found", SevError, HintNone, nil, "Repository is not known to this daemon", "Daemon nie zna tego repozytorium"},
	{"PROTO-0001", "proto.parse_error", SevError, HintNone, nil, "Request could not be parsed", "Daemon nie odczytał żądania"},
	{"PROTO-0001", "proto.invalid_envelope", SevError, HintNone, nil, "Request envelope is invalid", "Daemon odrzucił nieprawidłową kopertę"},
	{"PROTO-0001", "proto.invalid_payload", SevError, HintNone, nil, "Request payload is invalid", "Daemon odrzucił nieprawidłowe dane operacji"},
	{"PROTO-0001", "proto.missing_repo_id", SevError, HintNone, nil, "Repository id is required", "Brak identyfikatora repozytorium"},
	{"PROTO-0001", "proto.repo_not_found", SevError, HintNone, nil, "Repository is not known to this daemon", "Daemon nie zna tego repozytorium"},

	{"RECOVERY-0001", "recovery.unavailable", SevError, HintNone, nil, "Recovery service is not available", "Odzyskiwanie jest teraz niedostępne"},
	{"RECOVERY-1001", "recovery.download_failed", SevError, HintRequireAction, nil, "Recovery download failed", "Pobranie archiwum nie powiodło się"},

	{"REALM-0001", "realm.alias_unavailable", SevError, HintRetry, nil, "Realm alias service is not available", "Usługa aliasu strefy jest niedostępna"},
	{"REALM-0001", "realm.remove_unavailable", SevError, HintNone, nil, "Realm removal is not available", "Usunięcie strefy jest teraz niedostępne"},
	{"REALM-0002", "realm.not_activated", SevError, HintNone, nil, "Server is not activated", "Ten serwer nie jest aktywowany"},
	{"REALM-1001", "realm.alias_rejected", SevError, HintRequireAction, nil, "Realm alias was rejected", "Serwer odrzucił alias strefy"},
	{"REALM-1001", "realm.remove_begin_failed", SevError, HintRequireAction, nil, "Realm removal could not start", "Nie udało się rozpocząć usuwania strefy"},
	{"REALM-1002", "realm.remove_confirm_failed", SevError, HintRequireAction, nil, "Realm removal confirmation failed", "Potwierdzenie usunięcia strefy nie powiodło się"},
	{"REALM-2001", "realm.alias_required", SevError, HintRequireAction, nil, "A stable realm alias is required", "Przed blokowaniem plików ustaw stały alias strefy"},

	{"SERVER-0001", "server.detach_unavailable", SevError, HintNone, nil, "Server detach is not available", "Odłączanie serwera jest teraz niedostępne"},
	{"SERVER-0003", "server.session_timeout_unavailable", SevError, HintRetry, nil, "Session timeout is not available", "Nie można teraz zmienić limitu czasu wysyłki i pobierania"},
	{"SERVER-1002", "server.session_timeout_invalid", SevError, HintRequireAction, nil, "Session timeout is invalid", "Podaj liczbę minut od 1 do 1440"},
	{"SERVER-1003", "server.session_timeout_failed", SevError, HintRequireAction, nil, "Session timeout could not be saved", "Nie udało się zapisać limitu czasu"},
	{"SERVER-0002", "server.not_activated", SevError, HintNone, nil, "Server is not activated", "Ten serwer nie jest aktywowany"},
	{"SERVER-1001", "server.detach_failed", SevError, HintRequireAction, nil, "Server detach failed", "Odłączenie serwera nie powiodło się"},

	{"LOCK-2101", "reservation.list_failed", SevError, HintRetry, []string{"repo_id", "detail"}, "Reservation list failed", "Nie udało się pobrać listy rezerwacji"},
	{"LOCK-2102", "reservation.invalid_path", SevError, HintNone, nil, "Reservation path is invalid", "Ścieżka rezerwacji jest nieprawidłowa"},
	{"LOCK-2103", "reservation.release_failed", SevError, HintRequireAction, []string{"detail"}, "Reservation release failed", "Nie udało się zwolnić rezerwacji"},
	{"LOCK-2104", "reservation.projection_stale", SevWarn, HintRetryBackoff, []string{"server_id", "repo_id", "detail"}, "Reservation refresh failed; serving last known projection", "Odświeżenie rezerwacji nie powiodło się — pokazuję ostatnią znaną projekcję"},
	{"LOCK-2105", "reservation.projection_corrupt", SevError, HintNone, []string{"server_id", "detail"}, "Reservation projection artifact is corrupt or unreadable", "Zapisana projekcja rezerwacji jest uszkodzona lub nieczytelna"},
	{"LOCK-2106", "reservation.projection_write_failed", SevWarn, HintNone, []string{"server_id", "detail"}, "Reservation projection could not be persisted", "Nie udało się zapisać projekcji rezerwacji na dysku"},

	{"GRANT-0001", "realm.grants_unavailable", SevError, HintRetry, nil, "Grant service is not available", "Usługa uprawnień strefy jest niedostępna"},
	{"GRANT-1001", "realm.grant_recipients_unavailable", SevError, HintRetry, nil, "Grant recipient list failed", "Nie udało się pobrać listy odbiorców"},
	{"GRANT-1002", "realm.grant_rejected", SevError, HintRequireAction, nil, "Grant mutation was rejected", "Serwer odrzucił zmianę uprawnienia"},
	{"GRANT-1003", "realm.visibility_rejected", SevError, HintRequireAction, nil, "Visibility change was rejected", "Serwer odrzucił zmianę widoczności"},
	{"GRANT-2001", "realm.grants_forbidden", SevError, HintNone, nil, "Grant action is forbidden", "Brak uprawnień do tej operacji na strefie"},

	{"BRANDING-0001", "realm.branding_unavailable", SevError, HintRetry, nil, "Public branding is not available", "Wygląd udziałów jest teraz niedostępny"},
	{"BRANDING-1001", "realm.branding_rejected", SevError, HintRequireAction, nil, "Public branding was rejected", "Serwer odrzucił wygląd udziałów"},
	{"BRANDING-2001", "realm.branding_forbidden", SevError, HintNone, nil, "Public branding is forbidden", "Brak uprawnień do zmiany wyglądu udziałów"},

	{"POLICY-0001", "repo.editing_policy_unavailable", SevError, HintRetry, nil, "Editing policy service is not available", "Zmiana polityki blokad jest teraz niedostępna"},
	{"POLICY-2001", "repo.editing_policy_forbidden", SevError, HintNone, nil, "Editing policy change is forbidden", "Tylko właściciel może zmienić politykę blokad"},
	{"POLICY-2002", "repo.editing_policy_failed", SevError, HintRetry, nil, "Editing policy change failed", "Zmiana polityki blokad nie powiodła się"},

	{"SHARE-0001", "public_share.unavailable", SevError, HintRetry, nil, "Public share service is not available", "Udostępnienia publiczne są teraz niedostępne"},
	{"SHARE-0002", "public_share.list_all_unavailable", SevError, HintRetry, nil, "Public share aggregate is not available", "Zbiorcza lista udostępnień jest teraz niedostępna"},
	{"SHARE-1001", "public_share.list_failed", SevError, HintRetry, nil, "Public share list failed", "Nie udało się pobrać udostępnień"},
	{"SHARE-1002", "public_share.rejected", SevError, HintRequireAction, nil, "Public share mutation was rejected", "Serwer odrzucił zmianę udostępnienia"},
	{"SHARE-2001", "public_share.forbidden", SevError, HintNone, nil, "Public share action is forbidden", "Brak uprawnień do udostępnień publicznych"},

	{"UPLOAD-0001", "upload_channel.unavailable", SevError, HintRetry, nil, "Upload channel service is not available", "Półki przyjęcia są teraz niedostępne"},
	{"UPLOAD-1001", "upload_channel.list_failed", SevError, HintRetry, nil, "Upload channel list failed", "Nie udało się pobrać półek przyjęcia"},
	{"UPLOAD-1002", "upload_channel.rejected", SevError, HintRequireAction, nil, "Upload channel mutation was rejected", "Serwer odrzucił zmianę półki przyjęcia"},
	{"UPLOAD-2001", "upload_channel.forbidden", SevError, HintNone, nil, "Upload channel action is forbidden", "Brak uprawnień do półek przyjęcia"},

	{"SHOUT-1001", "shout.nothing_to_publish", SevInfo, HintNone, nil, "No pending changes to publish", "Nie ma nic do wysłania — folder jest już zgodny z serwerem"},
	{"SHOUT-1001", "shout.invalid_comment", SevError, HintRequireAction, nil, "Shout comment is invalid", "Komentarz wydania nie może być pusty, dłuższy niż 500 znaków ani zawierać znaków sterujących"},
	{"SHOUT-1002", "shout.read_only", SevError, HintNone, nil, "Repository is read-only", "To repozytorium jest tylko do odczytu"},
	{"SHOUT-1003", "shout.publish_failed", SevError, HintRequireAction, []string{"detail"}, "Shouting commit failed", "Nie udało się zapisać wydania na serwerze"},
	{"SHOUT-1004", "shout.list_failed", SevError, HintRetryLocal, nil, "Notice list failed", "Nie udało się odczytać listy wydań"},
	{"SHOUT-1005", "shout.ack_failed", SevError, HintRetryLocal, nil, "Notice acknowledgement failed", "Nie udało się potwierdzić wydania"},

	{"SYSTEM-0001", "system.lifecycle_unavailable", SevError, HintNone, nil, "Process lifecycle is not available", "Restart i zamknięcie są teraz niedostępne"},

	{"REPO-0001", "repo.lifecycle_unavailable", SevError, HintRetry, nil, "Repository lifecycle is not available", "Operacje na repozytorium są teraz niedostępne"},
	{"REPO-2001", "repo.create_forbidden", SevError, HintNone, nil, "Repository creation is forbidden", "Nie można utworzyć repozytorium na tym serwerze"},
	{"REPO-2002", "repo.invalid_local_intent", SevError, HintRequireAction, nil, "Local folder cannot be attached", "Wybrany folder lub lokalny stan repozytorium nie pozwala rozpocząć połączenia"},
	{"REPO-2003", "repo.already_attached", SevError, HintNone, nil, "Repository is already attached", "Repozytorium jest już połączone na tym kliencie"},
	{"REPO-2004", "repo.not_attachable", SevError, HintRetry, nil, "Repository is not attachable yet", "Repozytorium nie jest obecnie gotowe do połączenia"},
	{"REPO-2005", "repo.attachment_approval_failed", SevError, HintRequireAction, nil, "Attachment approval failed", "Daemon nie zatwierdził połączenia repozytorium"},
	{"REPO-2006", "repo.not_attached", SevError, HintNone, nil, "Repository is not attached", "Repozytorium nie jest podłączone na tym kliencie"},
	{"REPO-2007", "repo.relocation_failed", SevError, HintRequireAction, nil, "Working-copy relocation failed", "Przeniesienie kopii roboczej nie powiodło się"},
	{"REPO-2008", "repo.lifecycle_operation_not_found", SevError, HintNone, nil, "Lifecycle operation was not found", "Nie znaleziono tej operacji na repozytorium"},
	{"REPO-2010", "repo.detach_required_forbidden", SevError, HintNone, nil, "Required repository cannot be detached", "Wymaganego repozytorium nie można odłączyć"},
	{"REPO-2010", "repo.locate_failed", SevError, HintRequireAction, []string{"detail"}, "Moved working copy could not be rebound", "Nie udało się wskazać przeniesionej kopii roboczej"},
	{"REPO-2011", "repo.delete_forbidden", SevError, HintNone, nil, "Repository delete is forbidden", "Nie można trwale usunąć tego repozytorium"},
	{"REPO-2012", "repo.detach_failed", SevError, HintRequireAction, nil, "Detach failed", "Odłączenie repozytorium nie powiodło się"},
	{"REPO-2013", "repo.local_cleanup_pending", SevError, HintRetry, nil, "Repository was deleted on the server; local working-copy cleanup is pending", "Repozytorium usunięto z serwera; czyszczenie lokalnych metadanych kopii roboczej oczekuje na ponowienie"},
	{"REPO-2013", "repo.load_dump_forbidden", SevError, HintNone, nil, "Load-dump is forbidden", "Odtwarzanie z archiwum jest niedozwolone dla tego repozytorium"},
	{"REPO-2014", "repo.load_dump_failed", SevError, HintRequireAction, nil, "Load-dump failed", "Odtwarzanie z archiwum nie powiodło się"},
	{"REPO-2015", "repo.lifecycle_repair_forbidden", SevError, HintNone, nil, "Repository lifecycle repair is forbidden for the current durable state", "Tej niedokończonej operacji nie można naprawić w wybrany sposób"},
	{"REPO-2016", "repo.lifecycle_repair_failed", SevError, HintRequireAction, []string{"detail"}, "Repository lifecycle repair failed", "Nie udało się naprawić niedokończonej operacji na folderze"},
	{"REPO-3001", "repo.activity_unavailable", SevError, HintNone, nil, "Activity journal is not available", "Dziennik aktywności jest teraz niedostępny"},

	{"MOBILE-0001", "mobile_pairing.unavailable", SevError, HintRetry, nil, "Mobile pairing is not available", "Parowanie urządzenia mobilnego jest teraz niedostępne"},
	{"MOBILE-1001", "mobile_pairing.server_not_activated", SevError, HintNone, nil, "Mobile pairing needs an activated server", "Parowanie wymaga aktywowanego serwera"},
	{"MOBILE-1002", "mobile_pairing.begin_failed", SevError, HintRetry, nil, "Mobile pairing could not start", "Nie udało się rozpocząć parowania"},
	{"MOBILE-2001", KeyMobileOpNotOnServer, SevError, HintRequireAction, nil, "Mobile worker exited 70 — applied filees-mobile-v1 does not know this operation", "Serwer nie zna tej operacji mobilnej. Zwykle stary filees-mobile-v1 (status 70) — brakuje podpisanego apply."},
	{"MOBILE-2002", KeyMobileTreeNotIngested, SevError, HintRequireAction, nil, "UPLOAD_TREE reached the host but the worker does not ingest zip-on-wire yet", "Telefon spakował folder i wysłał jednym połączeniem. Serwer paczki jeszcze nie przyjmuje — brakuje apply filees-mobile-v1."},
	{"MOBILE-2003", KeyMobileTreeNotAPack, SevError, HintNone, nil, "UPLOAD_TREE payload is a repository zip, not a FileES tree pack", "To zwykły plik ZIP, nie paczka FileES. Taki artefakt idzie jako jeden obiekt, nie jako drzewo."},
	{"MOBILE-2004", KeyMobileTreeCorrupt, SevError, HintRetry, nil, "UPLOAD_TREE zip sha256 or size does not match the header", "Paczka uszkodziła się w transporcie (sha256 nie zgadza się z nagłówkiem). Nic nie zapisano — wyślij folder jeszcze raz."},

	{"WHALE-1001", KeyWhaleFailed, SevError, HintRetryBackoff, []string{"detail"}, "Whale operation failed", "Operacja dużego pliku nie powiodła się"},
	{"WHALE-2001", KeyWhalePathBusy, SevWarn, HintRetryBackoff, []string{"queue_position"}, "Another Whale generation owns this path", "Inna publikacja tego dużego pliku jest w toku"},
	{"WHALE-2002", KeyWhaleAccessDenied, SevError, HintNone, nil, "Whale repository access denied", "Brak uprawnień do tej operacji na dużym pliku"},
	{"WHALE-2003", KeyWhaleOffsetConflict, SevWarn, HintRetryLocal, []string{"offset"}, "Whale resume offset conflicts with durable state", "Wznawianie dużego pliku wymaga aktualnego offsetu serwera"},
	{"WHALE-2004", KeyWhaleDigestMismatch, SevError, HintRequireAction, nil, "Whale payload size or sha256 mismatch", "Duży plik nie zgadza się z przygotowaną generacją"},
	{"WHALE-2005", KeyWhaleInsufficientSpace, SevError, HintRequireAction, []string{"available_bytes", "required_bytes"}, "Whale storage reservation does not fit", "Serwer nie ma dość miejsca na operację dużego pliku"},

	{"ACTIVATION-0001", "activation.unavailable", SevError, HintRetry, nil, "Activation service is not available", "Aktywacja jest teraz niedostępna"},
	{"ACTIVATION-1001", "activation.begin_failed", SevError, HintRetry, nil, "Activation could not start", "Nie udało się rozpocząć aktywacji"},
	{"ACTIVATION-1002", "activation.finish_failed", SevError, HintRetry, nil, "Activation could not finish", "Nie udało się dokończyć aktywacji"},
	{"ACTIVATION-1003", "activation.pending_failed", SevError, HintRetry, nil, "Pending activation lookup failed", "Nie udało się odczytać oczekującej aktywacji"},
	{"ACTIVATION-1004", "activation.resume_failed", SevError, HintRetry, nil, "Activation resume failed", "Nie udało się wznowić aktywacji"},

	{"UPDATE-0001", "update.unavailable", SevError, HintNone, nil, "Update service is not available", "Aktualizacja jest teraz niedostępna"},
	{"UPDATE-1001", "update.status_failed", SevError, HintRetryBackoff, nil, "Update status failed", "Nie udało się sprawdzić aktualizacji"},
	{"UPDATE-1002", "update.plan_failed", SevError, HintRetryBackoff, nil, "Update plan failed", "Nie udało się przygotować planu aktualizacji"},
	{"UPDATE-1003", "update.apply_failed", SevError, HintRequireAction, nil, "Update apply failed", "Zastosowanie aktualizacji nie powiodło się"},
}
