package main

import (
	"fmt"
	"runtime"
	"sort"
	"strings"
	"unicode"
)

func trayTooltip(snapshot Snapshot, status string) string {
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
		reasons = append(reasons, name+trayCauseText(cause.Reason))
	}
	sort.Strings(reasons)
	if len(reasons) > 3 {
		reasons = append(reasons[:3], fmt.Sprintf("i %d kolejnych — szczegóły w panelu", len(reasons)-3))
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
	return limitTrayHint(text, limit)
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

func limitTrayHint(text string, limit int) string {
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
	const suffix = "… (więcej w panelu)"
	budget := limit - len([]rune(suffix))
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
func trayCauseText(reason string) string {
	switch reason {
	case "daemon_offline":
		return "brak połączenia z lokalnym klientem"
	case "refreshing":
		return "odświeżanie stanu klienta"
	case "announcements":
		return "nieprzeczytane ogłoszenia"
	case "metadata_cleanup_pending":
		return "sprzątanie metadanych czeka"
	case "preserved_copy_changed":
		return "repo usunięte; zachowana kopia ze zmianami"
	case "preserved_copy_unknown":
		return "repo usunięte; sprawdź zachowany folder"
	case "conflicts":
		return "konflikty do rozwiązania"
	case "working_copy_missing":
		return "nie znaleziono lokalnego folderu"
	case "interaction_required":
		return "wymagana decyzja użytkownika"
	case "degraded":
		return "synchronizacja wymaga naprawy"
	case "repository_offline":
		return "repozytorium niedostępne"
	case "access_revoked":
		return "dostęp do repozytorium cofnięty"
	case "initializing":
		return "trwa inicjalizacja"
	case "baselining":
		return "trwa ustalanie stanu początkowego"
	case "paused":
		return "praca wstrzymana"
	case "stopping":
		return "trwa zatrzymywanie"
	case "working":
		return "trwa praca nad repozytorium"
	default:
		return "stan wymaga sprawdzenia w panelu"
	}
}
