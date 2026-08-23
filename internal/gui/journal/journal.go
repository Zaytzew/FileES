// Package journal builds one presentation-safe chronology from daemon
// activity and structured errors. It deliberately owns aggregation so tray
// previews and the full native window cannot disagree.
package journal

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"filees/internal/gui/app"
)

const TrayLimit = 12

const connectivityErrorCode = "NET-4007"

type Entry struct {
	ID           string
	Timestamp    string
	Repo         string
	Summary      string
	Details      string
	Severity     string
	Emphasized   bool
	RelativeTime string
	ExactTime    string

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

// Build returns newest-first entries. Published paths from one repository
// revision form one entry. In-flight paths share a repo/stage entry. Failed
// activity carrying an ErrorID is folded into that structured error.
func Build(vm app.ViewModel) []Entry {
	return BuildAt(vm, time.Now())
}

// BuildAt is the deterministic form used by renderers and tests. Repeated
// connectivity warnings are one incident in the user journal; the raw
// errors.jsonl remains untouched for technical diagnostics.
func BuildAt(vm app.ViewModel, now time.Time) []Entry {
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
				entries = append(entries, errorEntry(record, group.repo, group.items))
				mergedErrors[group.errorID] = true
				continue
			}
		}
		entries = append(entries, activityEntry("activity:"+key, group))
	}
	for _, record := range vm.Errors {
		if record.Code == connectivityErrorCode {
			continue
		}
		if !mergedErrors[record.ID] {
			entries = append(entries, errorEntry(record, repoName(names, record.RepoID), nil))
		}
	}
	for _, group := range connectivity {
		entries = append(entries, connectivityEntry(group))
	}
	for _, notice := range vm.Notices {
		when := parseTime(notice.CreatedAt)
		entries = append(entries, Entry{
			ID:         "notice:" + notice.ID,
			Timestamp:  notice.CreatedAt,
			Repo:       repoName(names, notice.RepoID),
			Summary:    "Wydanie — " + notice.Title,
			Details:    "Oznacz jako przeczytane w menu FileES",
			Severity:   "notice",
			Emphasized: true,
			time:       when,
		})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if !entries[i].time.Equal(entries[j].time) {
			return entries[i].time.After(entries[j].time)
		}
		return entries[i].ID < entries[j].ID
	})
	for i := range entries {
		entries[i].RelativeTime = RelativeTimestamp(entries[i].Timestamp, now)
		entries[i].ExactTime = ExactTimestamp(entries[i].Timestamp)
	}
	return entries
}

func connectivityEntry(group *connectivityGroup) Entry {
	summary := fmt.Sprintf("Łączność · %s — brak połączenia z serwerem", group.repo)
	if group.count > 1 {
		summary += fmt.Sprintf(" · %d %s", group.count, plural(group.count, "zdarzenie", "zdarzenia", "zdarzeń"))
	}
	return Entry{
		ID: "connectivity:" + group.repoID, Timestamp: group.latest.Timestamp, Repo: group.repo,
		Summary:  summary,
		Details:  "FileES zachował zmiany lokalnie i automatycznie ponawiał połączenie. Surowe próby pozostają w logu diagnostycznym.",
		Severity: group.latest.Severity, Emphasized: false, time: group.time,
	}
}

// RelativeTimestamp renders compact, calm time labels for journal previews.
func RelativeTimestamp(value string, now time.Time) string {
	parsed := parseTime(value)
	if parsed.IsZero() {
		return value
	}
	localNow, localThen := now.Local(), parsed.Local()
	delta := localNow.Sub(localThen)
	if delta < time.Minute {
		return "przed chwilą"
	}
	if delta < 10*time.Minute {
		minutes := int(delta / time.Minute)
		if minutes == 1 {
			return "minutę temu"
		}
		return fmt.Sprintf("%d %s temu", minutes, plural(minutes, "minutę", "minuty", "minut"))
	}
	if sameDate(localThen, localNow) {
		return localThen.Format("15:04")
	}
	days := calendarDaysBetween(localThen, localNow)
	if days == 1 {
		return "wczoraj"
	}
	return fmt.Sprintf("%d %s temu", days, plural(days, "dzień", "dni", "dni"))
}

// ExactTimestamp is used by the expanded journal. The colon-separated date
// follows the accepted FileES UX contract literally: dd:mm:yy hh:mm.
func ExactTimestamp(value string) string {
	parsed := parseTime(value)
	if parsed.IsZero() {
		return value
	}
	return parsed.Local().Format("02:01:06 15:04")
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
	if record.Stage == "published" && record.Revision > 0 {
		return fmt.Sprintf("%s\x00published\x00%d", record.RepoID, record.Revision)
	}
	if record.Stage == "failed" && record.ErrorID != "" {
		return record.RepoID + "\x00failed\x00" + record.ErrorID
	}
	return record.RepoID + "\x00" + record.Stage
}

func activityEntry(id string, group *activityGroup) Entry {
	count := len(group.items)
	details := activityDetails(group.items)
	summary := ""
	if count == 1 {
		item := group.items[0]
		summary = fmt.Sprintf("%s / %s — %s", group.repo, item.Path, singleActivityLabel(item))
	} else {
		switch group.stage {
		case "published":
			summary = fmt.Sprintf("%s — publikacja: %d %s · r%d", group.repo, count, plural(count, "element", "elementy", "elementów"), group.revision)
		case "detected":
			summary = fmt.Sprintf("%s — wykryte zmiany: %d", group.repo, count)
		case "pending":
			summary = fmt.Sprintf("%s — oczekujące zmiany: %d", group.repo, count)
		case "publishing":
			summary = fmt.Sprintf("%s — publikowane zmiany: %d", group.repo, count)
		case "failed":
			summary = fmt.Sprintf("⚠ BŁĄD · %s — nieudane zmiany: %d", group.repo, count)
		default:
			summary = fmt.Sprintf("%s — %d zmian", group.repo, count)
		}
	}
	return Entry{ID: id, Timestamp: group.timestamp, Repo: group.repo, Summary: summary, Details: details, Emphasized: group.stage == "failed", time: group.time}
}

func errorEntry(record app.ErrorViewModel, repo string, activity []app.ActivityViewModel) Entry {
	details := errorHint(record.Hint)
	if paths := activityDetails(activity); paths != "" {
		if details != "" {
			details += "\n"
		}
		details += paths
	}
	return Entry{
		ID: "error:" + record.ID, Timestamp: record.Timestamp, Repo: repo,
		Summary: fmt.Sprintf("⚠ BŁĄD · %s — [%s] %s", repo, record.Code, record.Message),
		Details: details, Severity: record.Severity, Emphasized: true, time: parseTime(record.Timestamp),
	}
}

func errorHint(hint string) string {
	switch strings.TrimSpace(hint) {
	case "RETRY_LOCAL":
		return "Spróbuj ponownie"
	case "RETRY_BACKOFF":
		return "Ponowienie nastąpi później"
	case "REQUIRE_ACTION":
		return "Wymagane działanie użytkownika"
	case "ADMIN_ONLY":
		return "Skontaktuj się z administratorem"
	default:
		return strings.TrimSpace(hint)
	}
}

func plural(count int, one, few, many string) string {
	if count == 1 {
		return one
	}
	lastTwo := count % 100
	last := count % 10
	if last >= 2 && last <= 4 && (lastTwo < 12 || lastTwo > 14) {
		return few
	}
	return many
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
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return strings.Join(paths, "\n")
}

func singleActivityLabel(record app.ActivityViewModel) string {
	switch record.Stage {
	case "detected":
		return "wykryto lokalnie"
	case "pending":
		return "oczekuje na wysłanie"
	case "publishing":
		return "publikowanie"
	case "published":
		return fmt.Sprintf("%s · r%d", kindPastTense(record.Kind), record.Revision)
	case "failed":
		return "⚠ BŁĄD · nie udało się opublikować"
	default:
		return "stan nieznany"
	}
}

func kindPastTense(kind string) string {
	switch kind {
	case "added":
		return "dodano"
	case "modified":
		return "zaktualizowano"
	case "deleted":
		return "usunięto"
	case "renamed":
		return "zmieniono nazwę"
	default:
		return "opublikowano"
	}
}

func parseTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}
