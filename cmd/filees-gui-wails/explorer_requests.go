package main

import (
	"context"
	"time"

	"filees/internal/explorermenu"
	"filees/internal/gui/tray"
)

// A bounded launch queue survives cold startup without persisting/replaying
// gestures. The singleton forwards subsequent launches to this same queue.
var explorerRequests = make(chan explorermenu.Request, 16)

func queueExplorerRequest(request explorermenu.Request) bool {
	select {
	case explorerRequests <- request:
		return true
	default:
		return false
	}
}

func (service *GUIService) runExplorerRequests(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case request := <-explorerRequests:
			// The controller must see a fresh projection on a cold launch. A
			// timeout still reaches its explanatory refusal, never a mutation.
			deadline := time.NewTimer(30 * time.Second)
			poll := time.NewTicker(100 * time.Millisecond)
			ready := false
			for !ready {
				vm := service.viewModel()
				if vm.Connected && !vm.Stale {
					break
				}
				select {
				case <-ctx.Done():
					deadline.Stop()
					poll.Stop()
					return
				case <-deadline.C:
					ready = true
				case <-poll.C:
				}
			}
			deadline.Stop()
			poll.Stop()
			service.actionAdmission.RLock()
			service.mu.RLock()
			actions := service.actions
			service.mu.RUnlock()
			if !service.recoveringDisplay && actions != nil {
				kind := tray.IntentExplorerAdd
				if request.Verb == explorermenu.Detach {
					kind = tray.IntentExplorerDetach
				}
				select {
				case actions <- tray.Intent{Kind: kind, Path: request.Path}:
				default:
				}
			}
			service.actionAdmission.RUnlock()
		}
	}
}
