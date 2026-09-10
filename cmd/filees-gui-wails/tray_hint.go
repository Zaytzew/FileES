package main

import (
	"fmt"
	"runtime"
	"sort"
	"strings"
	"unicode"
	"unicode/utf16"
)

func trayTooltip(snapshot Snapshot, status string, locales ...nativeLanguage) string {
	language := nativePresentationLanguage(locales)
	var reasons []string
	for _, cause := range snapshot.trayCauses {
		name := ""
		for _, repo := range snapshot.Repositories {
			if repo.ID == cause.RepoID && repo.ServerID == cause.ServerID {
				name = firstNonBlank(repo.DisplayName, repo.ID)
				break
			}
		}
		if name != "" {
			name = trayHintLabel(name) + ": "
		}
		reasons = append(reasons, name+trayCauseText(cause.Reason, language))
	}
	sort.Strings(reasons)
	if len(reasons) > 3 {
		reasons = append(reasons[:3], language.format("tray.moreReasons", map[string]string{"count": fmt.Sprint(len(reasons) - 3)}))
	}
	text := "FileES — " + status
	if len(reasons) > 0 {
		// The reason goes first: platforms may limit the native tooltip length.
		text = "FileES — " + strings.Join(reasons, "; ") + "\n" + status
	}
	limit := 512
	if runtime.GOOS == "windows" {
		limit = 127 // native NOTIFYICONDATA.szTip: UTF-16 code units, excluding NUL
	}
	return limitTrayHint(text, limit, language)
}

func trayHintLabel(name string) string {
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, name)
	runes := []rune(strings.Join(strings.Fields(name), " "))
	if len(runes) > 40 {
		return string(runes[:39]) + "…"
	}
	return string(runes)
}

func limitTrayHint(text string, limit int, locales ...nativeLanguage) string {
	units := 0
	for _, r := range text {
		units++
		if r > 0xffff {
			units++
		}
	}
	if units <= limit {
		return text
	}
	suffix := nativePresentationLanguage(locales).text("tray.more")
	budget := limit - len(utf16.Encode([]rune(suffix)))
	if budget < 0 {
		suffix = ""
		budget = max(0, limit)
	}
	units = 0
	var out []rune
	for _, r := range text {
		n := 1
		if r > 0xffff {
			n = 2
		}
		if units+n > budget {
			break
		}
		out = append(out, r)
		units += n
	}
	return string(out) + suffix
}

// State labels are translated here, not guessed from raw diagnostic text.
func trayCauseText(reason string, locales ...nativeLanguage) string {
	language := nativePresentationLanguage(locales)
	switch reason {
	case "daemon_offline":
		return language.text("tray.cause.daemon_offline")
	case "refreshing":
		return language.text("tray.cause.refreshing")
	case "announcements":
		return language.text("tray.cause.announcements")
	case "metadata_cleanup_pending":
		return language.text("tray.cause.metadata_cleanup_pending")
	case "preserved_copy_changed":
		return language.text("tray.cause.preserved_copy_changed")
	case "preserved_copy_unknown":
		return language.text("tray.cause.preserved_copy_unknown")
	case "conflicts":
		return language.text("tray.cause.conflicts")
	case "working_copy_missing":
		return language.text("tray.cause.working_copy_missing")
	case "interaction_required":
		return language.text("tray.cause.interaction_required")
	case "degraded":
		return language.text("tray.cause.degraded")
	case "repository_offline":
		return language.text("tray.cause.repository_offline")
	case "access_revoked":
		return language.text("tray.cause.access_revoked")
	case "initializing":
		return language.text("tray.cause.initializing")
	case "baselining":
		return language.text("tray.cause.baselining")
	case "paused":
		return language.text("tray.cause.paused")
	case "stopping":
		return language.text("tray.cause.stopping")
	case "working":
		return language.text("tray.cause.working")
	default:
		return language.text("tray.cause.unknown")
	}
}
