// Package journal builds one presentation-safe chronology from daemon
// activity and structured errors. It deliberately owns aggregation so tray
// previews and the full native window cannot disagree.
package journal

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"filees/internal/gui/app"
	"filees/pkg/messagerender"
)

const TrayLimit = 12

const connectivityErrorCode = "NET-4007"

// Message is a sentence carried as data rather than as finished text: the
// interface catalogue key and the arguments it names.
//
// It exists because Go must not choose a plural form. The number of forms and
// the rule for picking one belong to a language, and a Go switch on the
// reader's language would be exactly the second source of wording that stage 2
// removed. A renderer with CLDR rules — the WebView, through Intl.PluralRules
// — selects the form from Args["count"].
//
// Entries carry a Message only where a count is inflected. Everywhere else the
// sentence is already one catalogue string and Summary or Details is enough.
type Message struct {
	Key  string            `json:"key"`
	Args map[string]string `json:"args,omitempty"`
}

type Entry struct {
	ID        string
	Timestamp string
	Repo      string
	Summary   string
	// Details is guidance a person can act on: the hint and the paths
	// involved. It is safe to show anywhere, including a tray tooltip.
	Details string
	// Diagnostics is the daemon's raw text. For a fault the catalogue cannot
	// name it is the only record of what happened, so the full journal shows
	// it — but it is kept out of Details because the tray must not carry raw
	// diagnostics, which is what internal/gui/app's model has always said.
	Diagnostics string
	// SummaryMessage and DetailsMessages repeat the sentence as data for a
	// renderer that can inflect a count. Where they are set, Summary and
	// Details hold the same sentence with the bare number instead — readable,
	// and all Go can honestly produce. A renderer prefers the message.
	SummaryMessage  *Message
	DetailsMessages []Message
	Severity        string
	Emphasized      bool
	RelativeTime    string
	ExactTime       string

	time time.Time
}

type connectivityGroup struct {
	repoID, repo string
	latest       app.ErrorViewModel
	time         time.Time
	count        int
}

type activityGroup struct {
	repoID, repo, stage, errorID string
	revision                     int64
	time                         time.Time
	timestamp                    string
	items                        []app.ActivityViewModel
}

// Texts resolves the wording the journal composes.
//
// The two resolvers are deliberately separate because the two catalogues are:
// Chrome is the renderer's own interface wording, Hint belongs to the daemon,
// which owns what a hint means and serves its sentence per language. A journal
// that wrote hint text itself would be a second source for wording the daemon
// already publishes.
//
// A zero Texts keeps the built-in fallbacks. They exist so a caller without a
// catalogue — a test, or a renderer during start-up before the first snapshot
// — still produces readable entries rather than blanks.
type Texts struct {
	Chrome func(key, fallback string) string
	Hint   func(hint string) string
}

// namedArgument matches the {name} placeholders the interface catalogue uses
// for messages a renderer composes. Keys resolved straight through chrome keep
// printf verbs, because Go formats those itself.
var namedArgument = regexp.MustCompile(`\{([a-zA-Z][\w]*)\}`)

// message builds the data form of a sentence and the plain text that stands in
// for it. Both come from one Args map, so the two cannot drift apart: the
// renderer inflects plainKey's counted noun, and Go never tries to.
func (t Texts) message(key, plainKey, plainFallback string, args map[string]string) (*Message, string) {
	plain := namedArgument.ReplaceAllStringFunc(t.chrome(plainKey, plainFallback), func(token string) string {
		if value, ok := args[token[1:len(token)-1]]; ok {
			return value
		}
		return token
	})
	return &Message{Key: key, Args: args}, plain
}

func (t Texts) chrome(key, fallback string) string {
	if t.Chrome == nil {
		return fallback
	}
	if value := t.Chrome(key, fallback); value != "" {
		return value
	}
	return fallback
}

func (t Texts) hintText(hint string) string {
	if t.Hint == nil {
		return ""
	}
	return t.Hint(hint)
}

// Build returns newest-first entries. Published paths from one repository
// revision form one entry. In-flight paths share a repo/stage entry. Failed
// activity carrying an ErrorID is folded into that structured error.
func Build(vm app.ViewModel, texts Texts) []Entry {
	return BuildAt(vm, time.Now(), texts)
}

// BuildAt is the deterministic form used by renderers and tests. Repeated
// connectivity warnings are one incident in the user journal; the raw
// errors.jsonl remains untouched for technical diagnostics.
func BuildAt(vm app.ViewModel, now time.Time, texts Texts) []Entry {
	names := repositoryNames(vm)
	errorsByID := make(map[string]app.ErrorViewModel, len(vm.Errors))
	connectivity := make(map[string]*connectivityGroup)
	for _, record := range vm.Errors {
		if record.Code == connectivityErrorCode {
			group := connectivity[record.RepoID]
			when := parseTime(record.Timestamp)
			if group == nil {
				group = &connectivityGroup{repoID: record.RepoID, repo: repoName(names, record.RepoID)}
				connectivity[record.RepoID] = group
			}
			group.count++
			if when.After(group.time) || group.latest.Timestamp == "" {
				group.latest, group.time = record, when
			}
			continue
		}
		errorsByID[record.ID] = record
	}

	groups := make(map[string]*activityGroup)
	for _, record := range vm.Activity {
		key := activityKey(record)
		group := groups[key]
		if group == nil {
			group = &activityGroup{repoID: record.RepoID, repo: repoName(names, record.RepoID), stage: record.Stage, revision: record.Revision, errorID: record.ErrorID}
			groups[key] = group
		}
		when := parseTime(record.UpdatedAt)
		if when.After(group.time) || group.timestamp == "" {
			group.time, group.timestamp = when, record.UpdatedAt
		}
		group.items = append(group.items, record)
	}

	entries := make([]Entry, 0, len(groups)+len(vm.Errors))
	mergedErrors := make(map[string]bool)
	for key, group := range groups {
		if group.stage == "failed" && group.errorID != "" {
			if record, ok := errorsByID[group.errorID]; ok {
				entries = append(entries, errorEntry(record, group.repo, group.items, texts))
				mergedErrors[group.errorID] = true
				continue
			}
		}
		entries = append(entries, activityEntry("activity:"+key, group, texts))
	}
	for _, record := range vm.Errors {
		if record.Code == connectivityErrorCode {
			continue
		}
		if !mergedErrors[record.ID] {
			entries = append(entries, errorEntry(record, repoName(names, record.RepoID), nil, texts))
		}
	}
	for _, group := range connectivity {
		entries = append(entries, connectivityEntry(group, texts))
	}
	for _, notice := range vm.Notices {
		when := parseTime(notice.CreatedAt)
		entries = append(entries, Entry{
			ID:         "notice:" + notice.ID,
			Timestamp:  notice.CreatedAt,
			Repo:       repoName(names, notice.RepoID),
			Summary:    fmt.Sprintf(texts.chrome("journal.notice", "Wydanie — %s"), notice.Title),
			Details:    texts.chrome("journal.noticeDetail", "Oznacz jako przeczytane w menu FileES"),
			Severity:   "notice",
			Emphasized: true,
			time:       when,
		})
	}
	for _, record := range vm.Detachments {
		entries = append(entries, detachmentEntry(record, texts))
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if !entries[i].time.Equal(entries[j].time) {
			return entries[i].time.After(entries[j].time)
		}
		return entries[i].ID < entries[j].ID
	})
	for i := range entries {
		entries[i].RelativeTime = RelativeTimestamp(entries[i].Timestamp, now, texts)
		entries[i].ExactTime = ExactTimestamp(entries[i].Timestamp)
	}
	return entries
}

// detachmentEntry is the one place a detached server is named on purpose.
//
// The journal is a chronology somebody chooses to read, not a badge standing
// over the projection, which is why an entry here was agreed while a banner
// was refused. What r789 established is that a detachment is a decision rather
// than news; a decision still belongs in the record of what happened.
//
// The two causes get different sentences because their cures are opposite. A
// self-detachment is finished business and says so. A revoked client has
// something left to do, and the entry must not imply that the moment shown is
// when the server decided - only when this client found out.
func detachmentEntry(record app.DetachmentViewModel, texts Texts) Entry {
	when := parseTime(record.At)
	entry := Entry{
		ID:        "detachment:" + record.ServerID,
		Timestamp: record.At,
		Severity:  "notice",
		time:      when,
	}
	detailKey := "journal.detachedSelfDetail"
	if record.SelfDetached() {
		entry.Summary = fmt.Sprintf(texts.chrome("journal.detachedSelf", "Odłączono od serwera „%s”"), record.Name())
		entry.Details = texts.chrome(detailKey, "Serwer unieważnił klucz tej instalacji, a lokalny profil został usunięty.")
	} else {
		detailKey = "journal.detachedByServerDetail"
		entry.Summary = fmt.Sprintf(texts.chrome("journal.detachedByServer", "Serwer „%s” odłączył tego klienta"), record.Name())
		entry.Details = texts.chrome(detailKey, "Zauważone o tej godzinie; wymagana ponowna aktywacja klienta.")
	}
	// The folders are the one number here, and it is a sentence of its own
	// rather than a fragment glued to the one above: a language may need a
	// different order, and joining two finished sentences leaves it free to.
	if count := len(record.WorkingCopies); count > 0 {
		kept, plain := texts.message("journal.detachedFilesKept", "journal.detachedFilesKeptPlain",
			"Pliki zostały na dysku — {count}.", map[string]string{"count": strconv.Itoa(count)})
		entry.Details += " " + plain
		entry.DetailsMessages = []Message{{Key: detailKey}, *kept}
	}
	return entry
}

func connectivityEntry(group *connectivityGroup, texts Texts) Entry {
	summary := fmt.Sprintf(texts.chrome("journal.connectivity", "Łączność · %s — brak połączenia z serwerem"), group.repo)
	// Repeated interruptions are one incident with a count, and the count sits
	// inside the sentence rather than after it, so the whole sentence is one
	// catalogue entry and a language may put the number where it belongs.
	var counted *Message
	if group.count > 1 {
		counted, summary = texts.message("journal.connectivityCounted", "journal.connectivityCountedPlain",
			"Łączność · {repo} — brak połączenia z serwerem · {count}",
			map[string]string{"repo": group.repo, "count": strconv.Itoa(group.count)})
	}
	return Entry{
		ID: "connectivity:" + group.repoID, Timestamp: group.latest.Timestamp, Repo: group.repo,
		Summary:        summary,
		SummaryMessage: counted,
		Details:        texts.chrome("journal.connectivityDetail", "FileES zachował zmiany lokalnie i automatycznie ponawiał połączenie. Surowe próby pozostają w logu diagnostycznym."),
		Severity:       group.latest.Severity, Emphasized: false, time: group.time,
	}
}

// RelativeTimestamp renders compact, calm time labels for journal previews.
//
// "N minutes ago" and "N days ago" are deliberately absent. Saying them needs
// both a plural rule and a relative-time vocabulary per language, and the
// renderer already has both: the WebView recomputes every journal timestamp
// with Intl.RelativeTimeFormat and only falls back to this string for a
// timestamp it cannot parse. So the counted phrases become the clock or the
// date — no number that would have to agree with a noun.
func RelativeTimestamp(value string, now time.Time, texts Texts) string {
	parsed := parseTime(value)
	if parsed.IsZero() {
		return value
	}
	localNow, localThen := now.Local(), parsed.Local()
	delta := localNow.Sub(localThen)
	if delta < time.Minute {
		return texts.chrome("journal.time.justNow", "przed chwilą")
	}
	if sameDate(localThen, localNow) {
		return localThen.Format("15:04")
	}
	if calendarDaysBetween(localThen, localNow) == 1 {
		return texts.chrome("journal.time.yesterday", "wczoraj")
	}
	return localThen.Format("02:01 15:04")
}

// ExactTimestamp is used by the expanded journal. A four-digit year keeps
// archival entries unambiguous.
func ExactTimestamp(value string) string {
	parsed := parseTime(value)
	if parsed.IsZero() {
		return value
	}
	return parsed.Local().Format("02:01:2006 15:04")
}

func sameDate(left, right time.Time) bool {
	ly, lm, ld := left.Date()
	ry, rm, rd := right.Date()
	return ly == ry && lm == rm && ld == rd
}

func calendarDaysBetween(then, now time.Time) int {
	start := time.Date(then.Year(), then.Month(), then.Day(), 0, 0, 0, 0, then.Location())
	end := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	days := int(end.Sub(start).Hours() / 24)
	if days < 1 {
		return 1
	}
	return days
}

func repositoryNames(vm app.ViewModel) map[string]string {
	names := make(map[string]string, len(vm.Repos))
	for _, repo := range vm.Repos {
		name := strings.TrimSpace(repo.DisplayName)
		if name == "" {
			name = repo.ID
		}
		names[repo.ID] = name
	}
	return names
}

func repoName(names map[string]string, repoID string) string {
	if name := names[repoID]; name != "" {
		return name
	}
	if strings.TrimSpace(repoID) != "" {
		return repoID
	}
	return "FileES"
}

func activityKey(record app.ActivityViewModel) string {
	if (record.Stage == "published" || record.Stage == "received") && record.Revision > 0 {
		return fmt.Sprintf("%s\x00%s\x00%d", record.RepoID, record.Stage, record.Revision)
	}
	if record.Stage == "failed" && record.ErrorID != "" {
		return record.RepoID + "\x00failed\x00" + record.ErrorID
	}
	return record.RepoID + "\x00" + record.Stage
}

func activityEntry(id string, group *activityGroup, texts Texts) Entry {
	count := len(group.items)
	details := activityDetails(group.items)
	summary := ""
	var counted *Message
	if count == 1 {
		item := group.items[0]
		summary = fmt.Sprintf("%s / %s — %s", group.repo, item.Path, singleActivityLabel(item, texts))
	} else {
		// A counted noun goes through message; a bare number does not need to.
		args := map[string]string{"repo": group.repo, "count": strconv.Itoa(count), "revision": strconv.FormatInt(group.revision, 10)}
		switch group.stage {
		case "published":
			counted, summary = texts.message("journal.queuePublished", "journal.queuePublishedPlain",
				"{repo} — publikacja: {count} · r{revision}", args)
		case "received":
			counted, summary = texts.message("journal.queueReceived", "journal.queueReceivedPlain",
				"{repo} — pobrano zmiany: {count} · r{revision}", args)
		case "reconciled":
			counted, summary = texts.message("journal.queueReconciled", "journal.queueReconciledPlain",
				"{repo} — uzgodniono stan: {count} (bez wysyłania)", args)
		case "detected":
			summary = fmt.Sprintf(texts.chrome("journal.queueDetected", "%s — wykryte zmiany: %d"), group.repo, count)
		case "pending":
			summary = fmt.Sprintf(texts.chrome("journal.queuePending", "%s — oczekujące zmiany: %d"), group.repo, count)
		case "publishing":
			summary = fmt.Sprintf(texts.chrome("journal.queuePublishing", "%s — publikowane zmiany: %d"), group.repo, count)
		case "failed":
			summary = fmt.Sprintf(texts.chrome("journal.queueFailed", "%s · %s — nieudane zmiany: %d"), texts.chrome("journal.errorPrefix", "⚠ BŁĄD"), group.repo, count)
		default:
			counted, summary = texts.message("journal.queueChanges", "journal.queueChangesPlain",
				"{repo} — zmiany: {count}", args)
		}
	}
	return Entry{ID: id, Timestamp: group.timestamp, Repo: group.repo, Summary: summary, SummaryMessage: counted, Details: details, Emphasized: group.stage == "failed", time: group.time}
}

func errorEntry(record app.ErrorViewModel, repo string, activity []app.ActivityViewModel, texts Texts) Entry {
	details := texts.hintText(record.Hint)
	if paths := activityDetails(activity); paths != "" {
		if details != "" {
			details += "\n"
		}
		details += paths
	}
	return Entry{
		ID: "error:" + record.ID, Timestamp: record.Timestamp, Repo: repo,
		Summary: fmt.Sprintf("%s · %s — [%s] %s", texts.chrome("journal.errorPrefix", "⚠ BŁĄD"), repo, record.Code, record.Message),
		Details: details, Diagnostics: strings.TrimSpace(record.Details),
		Severity: record.Severity, Emphasized: true, time: parseTime(record.Timestamp),
	}
}

func activityDetails(items []app.ActivityViewModel) string {
	if len(items) == 0 {
		return ""
	}
	paths := make([]string, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		path := strings.TrimSpace(item.Path)
		if path != "" && !seen[path] {
			seen[path] = true
			if item.Size != nil {
				path += " · " + messagerender.FormatBytes(*item.Size)
			}
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return strings.Join(paths, "\n")
}

func singleActivityLabel(record app.ActivityViewModel, texts Texts) string {
	switch record.Stage {
	case "detected":
		return texts.chrome("journal.stage.detected", "wykryto lokalnie")
	case "pending":
		return texts.chrome("journal.stage.pending", "oczekuje na wysłanie")
	case "publishing":
		return texts.chrome("journal.stage.publishing", "publikowanie")
	case "published":
		return fmt.Sprintf(texts.chrome("journal.stage.published", "%s · r%d"), kindPastTense(record.Kind, texts), record.Revision)
	case "received":
		return fmt.Sprintf(texts.chrome("journal.stage.received", "pobrano zmianę: %s · r%d"), kindPastTense(record.Kind, texts), record.Revision)
	case "reconciled":
		return texts.chrome("journal.stage.reconciled", "uzgodniono stan (bez wysyłania)")
	case "failed":
		return fmt.Sprintf(texts.chrome("journal.stage.failed", "%s · nie udało się opublikować"), texts.chrome("journal.errorPrefix", "⚠ BŁĄD"))
	default:
		return texts.chrome("journal.stage.unknown", "stan nieznany")
	}
}

func kindPastTense(kind string, texts Texts) string {
	switch kind {
	case "added":
		return texts.chrome("journal.kind.added", "dodano")
	case "modified":
		return texts.chrome("journal.kind.modified", "zaktualizowano")
	case "deleted":
		return texts.chrome("journal.kind.deleted", "usunięto")
	case "renamed":
		return texts.chrome("journal.kind.renamed", "zmieniono nazwę")
	default:
		return texts.chrome("journal.kind.published", "opublikowano")
	}
}

func parseTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}
