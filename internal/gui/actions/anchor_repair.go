package actions

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"filees/internal/gui/app"
	"filees/internal/gui/platform"
	"filees/internal/gui/tray"
)

// AnchorRepairOutcome is what the elevated repair of one virtual folder
// ended with. The GUI learns nothing else from that process.
type AnchorRepairOutcome int

const (
	AnchorRepairDone AnchorRepairOutcome = iota
	// AnchorRepairNoRights: Windows gave no administrator rights (declined,
	// cancelled, or no such account). Nothing was changed.
	AnchorRepairNoRights
	// AnchorRepairFailed: nothing was removed and the filter is attached.
	AnchorRepairFailed
	// AnchorRepairIncomplete: some entries stayed; the filter is attached.
	AnchorRepairIncomplete
	// AnchorRepairFilterDetached: cloud files are still switched off on the
	// volume.
	AnchorRepairFilterDetached
)

// After a successful repair the daemon looks at the folder again within about
// ten seconds; the card keeps its spinner until then, bounded.
const (
	anchorRepairSettle     = 90 * time.Second
	anchorRepairSettlePoll = 250 * time.Millisecond
)

// AnchorRepairer starts the elevated repair. It is given a folder and
// nothing else; with reattachOnly it only switches cloud files back on.
type AnchorRepairer interface {
	RepairAnchor(ctx context.Context, root string, reattachOnly bool) (AnchorRepairOutcome, error)
}

// startRepairAnchor is the consent screen and its outcomes
// (implementation notes (not distributed)). It asks; Windows then asks
// for administrator rights in its own window; without them nothing changes.
func (c *Controller) startRepairAnchor(ctx context.Context, serverID, repoID string) {
	key := "repair-anchor:" + serverID + ":" + repoID
	if serverID == "" || repoID == "" || c.cfg.AnchorRepairer == nil || c.cfg.Prompter == nil || !c.beginOperation(key) {
		return
	}
	c.tasks.Add(1)
	go func() {
		defer c.tasks.Done()
		defer c.endOperation(key)
		damaged := func() (name, root string, entries int, ok bool) {
			for _, repo := range c.cfg.ViewModel().Repos {
				if repo.ID != repoID || repo.ServerID != serverID {
					continue
				}
				if !repo.Attached || repo.AnchorDamage == nil || repo.AnchorDamage.Entries < 1 || !filepath.IsAbs(repo.LocalPath) {
					return "", "", 0, false
				}
				name = repo.DisplayName
				if strings.TrimSpace(name) == "" {
					name = repo.ID
				}
				return name, filepath.Clean(repo.LocalPath), repo.AnchorDamage.Entries, true
			}
			return "", "", 0, false
		}
		name, root, entries, ok := damaged()
		if !ok {
			return
		}
		args := map[string]string{"name": name, "path": root, "count": strconv.Itoa(entries), "volume": filepath.VolumeName(root)}
		confirmed, err := c.cfg.Prompter.Confirm(ctx, platform.ConfirmRequest{
			PresentationKey: "confirm.repairAnchor", PresentationArgs: args,
			Title:       "Windows nie odczytuje części plików w tym folderze",
			Text:        "FileES usunie " + args["count"] + " nieczytelnych wpisów z folderu " + root + ". Windows na chwilę wyłączy pliki w chmurze na całym dysku " + args["volume"] + ". Potrzebne są uprawnienia administratora; bez zgody nic nie zostanie zmienione.",
			ConfirmText: "Napraw — Windows poprosi o zgodę", CancelText: "Nie teraz",
		})
		if err != nil || !confirmed {
			return
		}
		// Stale GUI state is never a reason to elevate.
		if _, current, _, still := damaged(); !still || current != root {
			return
		}
		// From consent to a settled card the folder shows work in progress.
		// The repair itself is seconds, but the daemon then needs a moment to
		// see the folder clean and reconnect it, and a silent card reads as a
		// failure (owner, 2026-10-08).
		actionID := c.startProjectedAction(app.PendingAction{
			Kind: string(tray.IntentRepairAnchor), ServerID: serverID, RepoID: repoID,
			Label: c.uiText("pending.repairAnchor", "Naprawianie folderu wirtualnego"),
		})
		defer c.finishProjectedAction(actionID)
		outcome, err := c.cfg.AnchorRepairer.RepairAnchor(ctx, root, false)
		for attempt := 0; err == nil && outcome == AnchorRepairFilterDetached && attempt < 3; attempt++ {
			again, promptErr := c.cfg.Prompter.Confirm(ctx, platform.ConfirmRequest{
				PresentationKey: "confirm.repairAnchorReattach", PresentationArgs: args,
				Title:       "Pliki w chmurze na dysku " + args["volume"] + " są nadal wyłączone",
				Text:        "Windows nie włączył ich z powrotem. Niepobrane pliki na tym dysku nie otworzą się, dopóki tego nie przywrócisz. Wystarczy ponowić albo uruchomić komputer ponownie.",
				ConfirmText: "Włącz ponownie — Windows poprosi o zgodę", CancelText: "Zamknij",
			})
			if promptErr != nil || !again {
				return
			}
			outcome, err = c.cfg.AnchorRepairer.RepairAnchor(ctx, root, true)
			if err == nil && outcome == AnchorRepairNoRights {
				outcome = AnchorRepairFilterDetached // still off; ask again
			}
		}
		info := func(presentation, title, text string) {
			_ = c.cfg.Prompter.ShowInfo(ctx, platform.InfoRequest{PresentationKey: presentation, PresentationArgs: args, Title: title, Text: text})
		}
		switch {
		case err != nil:
			c.reportActionError(ctx, key, c.uiText("feedback.repairAnchorFailed", "Nie udało się naprawić folderu"), name+" — "+c.actionErrorBody(err))
		case outcome == AnchorRepairDone:
			// Say "repaired" when the card can show it, not before.
			settle := c.cfg.AnchorRepairSettle
			if settle <= 0 {
				settle = anchorRepairSettle
			}
			for deadline := time.Now().Add(settle); time.Now().Before(deadline) && ctx.Err() == nil; {
				if _, _, _, still := damaged(); !still {
					break
				}
				time.Sleep(anchorRepairSettlePoll)
			}
			info("info.repairAnchorDone", "Folder "+name+" jest naprawiony", "Nieczytelne wpisy zostały usunięte, a pliki w chmurze na dysku "+args["volume"]+" są znowu włączone. FileES połączy foldery wirtualne i odtworzy usunięte wpisy w ciągu około minuty.")
		case outcome == AnchorRepairNoRights:
			info("info.repairAnchorNoRights", "Brak uprawnień do operacji", "Windows nie przyznał uprawnień administratora. Nic nie zostało zmienione: nieczytelne pliki w folderze "+name+" nadal się nie otworzą, pozostałe działają.")
		case outcome == AnchorRepairIncomplete:
			info("info.repairAnchorIncomplete", "Część wpisów została", "Nie wszystkie nieczytelne wpisy w folderze "+name+" dały się usunąć. Pliki w chmurze na dysku "+args["volume"]+" są włączone.")
		case outcome == AnchorRepairFilterDetached:
			info("info.repairAnchorDetached", "Pliki w chmurze na dysku "+args["volume"]+" są nadal wyłączone", "Uruchom komputer ponownie albo ponów naprawę folderu "+name+".")
		default:
			info("info.repairAnchorUnchanged", "Nie udało się naprawić folderu", "Nic nie zostało usunięte z folderu "+name+". Pliki w chmurze na dysku "+args["volume"]+" są włączone.")
		}
	}()
}
