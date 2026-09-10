package actions

import (
	"context"
	"encoding/json"
	"filees/internal/gui/app"
	"filees/internal/gui/platform"
	"fmt"
	"strings"
	"time"
)

// Presentation-only DTOs. The adapter maps the daemon contract; the controller
// neither reads working copies nor infers renames from names or file contents.
type IntentResolutionPlan struct {
	ID, RepoID, Choice string
	Paths              []IntentResolutionPath
}
type IntentResolutionPath struct {
	Path, Operation string
	Size            int64
}
type IntentResolver interface {
	PlanIntents(context.Context, string) (*IntentResolutionPlan, error)
	ApplyIntents(context.Context, string, string, string) error
}

func (c *Controller) startResolveIntents(ctx context.Context, serverID, repoID string) {
	key := "resolve-intents:" + serverID + ":" + repoID
	if c.cfg.IntentResolver == nil || c.cfg.Prompter == nil || !c.beginOperation(key) {
		return
	}
	c.tasks.Add(1)
	go func() {
		defer c.tasks.Done()
		defer c.endOperation(key)
		vm := c.cfg.ViewModel()
		repo, ok := findRepo(vm, repoID)
		if !ok || repo.ServerID != serverID || !repo.Attached || repo.Access != "rw" || !vm.Connected || vm.Stale || !vm.CanResolveIntents() {
			return
		}
		readCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		plan, err := c.cfg.IntentResolver.PlanIntents(readCtx, repoID)
		cancel()
		if err != nil {
			c.reportActionError(ctx, key, c.uiText("intent.planFailed", "Nie można przygotować planu zmian"), actionErrorBody(err))
			return
		}
		if plan == nil || plan.ID == "" || plan.RepoID != repoID || plan.Choice != "delete_add" || len(plan.Paths) == 0 {
			return
		}
		var text strings.Builder
		text.WriteString(firstNonBlank(repo.DisplayName, repo.ID) + "\n\nPotwierdzasz niezależne usunięcia i dodania, nie przeniesienie z zachowaniem historii pliku.\n")
		for _, path := range plan.Paths {
			switch path.Operation {
			case "add":
				fmt.Fprintf(&text, "\nDODAJ: %s (%d B)", path.Path, path.Size)
			case "delete":
				fmt.Fprintf(&text, "\nUSUŃ: %s", path.Path)
			default:
				return
			}
		}
		text.WriteString("\n\nPo potwierdzeniu FileES ponownie sprawdzi plan i wznowi zwykłą kolejkę wysyłki. Anulowanie niczego nie zmienia.")
		paths, err := json.Marshal(plan.Paths)
		if err != nil {
			return
		}
		confirmed, err := c.cfg.Prompter.Confirm(ctx, platform.ConfirmRequest{PresentationKey: "details.intent", PresentationArgs: map[string]string{"name": firstNonBlank(repo.DisplayName, repo.ID), "paths": string(paths)}, Title: "Rozstrzygnij zmiany plików", Text: text.String(), ConfirmText: "Potwierdź usunięcia i dodania", CancelText: "Anuluj"})
		if err != nil || !confirmed {
			return
		}
		applyCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err = c.cfg.IntentResolver.ApplyIntents(applyCtx, repoID, plan.ID, plan.Choice)
		cancel()
		if err != nil {
			c.reportActionError(ctx, key, c.uiText("intent.rejected", "Decyzja nie została przyjęta"), actionErrorBody(err))
			return
		}
		actionID := c.startProjectedAction(app.PendingAction{Kind: "resolve_intents", ServerID: serverID, RepoID: repoID, Label: c.uiText("intent.refreshing", "Odświeżanie rozstrzygniętych zmian"), ExpectedIntentsResolved: true})
		c.awaitProjectedAction(actionID)
		if actionID == "" && c.cfg.Refresh != nil {
			c.cfg.Refresh()
		}
		c.notify(ctx, platform.Notification{ID: key, Group: key, Title: c.uiText("intent.saved", "Interpretacja zmian zapisana"), Body: c.uiText("intent.queued", "Pliki wróciły do zwykłej kolejki wysyłki. To nie jest jeszcze potwierdzenie publikacji."), Urgency: platform.UrgencyNormal})
	}()
}
