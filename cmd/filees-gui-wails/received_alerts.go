package main

import (
	"strconv"
	"strings"

	"filees/internal/gui/platform"
)

// receivedAlertPolicy announces in the tray what background updates brought
// in: one notification per repository and revision (owner, 2026-09-28 - the
// journal alone showed only his own commits). Groups already in the feed when
// the first fresh snapshot arrives are the baseline, and announced groups are
// remembered here rather than derived from the previous snapshot, so a
// reconnect that re-delivers the whole feed replays nothing.
type receivedAlertPolicy struct {
	initialized bool
	seen        map[string]bool
}

// receivedAlertMemory bounds the announced groups remembered; the journal
// keeps far fewer.
const receivedAlertMemory = 1000

type receivedAlertGroup struct {
	repoID   string
	revision int64
	paths    []string
}

func receivedAlertGroups(snapshot Snapshot) (map[string]*receivedAlertGroup, []string) {
	groups := make(map[string]*receivedAlertGroup)
	var order []string
	for _, record := range snapshot.Activity {
		if record.Stage != "received" || record.Revision <= 0 {
			continue
		}
		key := record.RepoID + ":" + strconv.FormatInt(record.Revision, 10)
		group := groups[key]
		if group == nil {
			group = &receivedAlertGroup{repoID: record.RepoID, revision: record.Revision}
			groups[key] = group
			order = append(order, key)
		}
		group.paths = append(group.paths, record.Path)
	}
	return groups, order
}

func (policy *receivedAlertPolicy) Observe(snapshot Snapshot, locales ...nativeLanguage) []platform.Notification {
	if !snapshot.Connected || snapshot.Stale {
		return nil
	}
	groups, order := receivedAlertGroups(snapshot)
	if !policy.initialized || policy.seen == nil || len(policy.seen) >= receivedAlertMemory {
		policy.initialized = true
		policy.seen = make(map[string]bool, len(groups))
		for key := range groups {
			policy.seen[key] = true
		}
		return nil
	}
	language := nativePresentationLanguage(locales)
	names := make(map[string]string, len(snapshot.Repositories))
	for _, repo := range snapshot.Repositories {
		if name := strings.TrimSpace(repo.DisplayName); name != "" {
			names[repo.ID] = name
		}
	}
	var result []platform.Notification
	for _, key := range order {
		if policy.seen[key] {
			continue
		}
		policy.seen[key] = true
		group := groups[key]
		name := names[group.repoID]
		if name == "" {
			name = group.repoID
		}
		const shown = 3
		files := make([]string, 0, shown)
		for i, path := range group.paths {
			if i == shown {
				break
			}
			if slash := strings.LastIndex(path, "/"); slash >= 0 {
				path = path[slash+1:]
			}
			files = append(files, path)
		}
		args := map[string]string{"name": name, "revision": strconv.FormatInt(group.revision, 10), "files": strings.Join(files, ", ")}
		body := language.format("notification.received.body", args)
		if more := len(group.paths) - len(files); more > 0 {
			args["count"] = strconv.Itoa(more)
			body = language.format("notification.received.bodyMore", args)
		}
		result = append(result, platform.Notification{
			ID: "activity.received." + key, Group: "activity.received",
			Title: language.text("notification.received.title"), Body: body,
			Urgency: platform.UrgencyLow,
		})
	}
	return result
}
