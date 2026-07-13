package tray

import (
	"context"
	"sync"
)

// Renderer applies complete MenuModels to a Backend. Every render cancels the
// click listeners belonging to the previous menu generation before ResetMenu.
type Renderer struct {
	backend Backend
	icons   IconSet
	intents chan<- Intent

	mu           sync.Mutex
	cancelRender context.CancelFunc
	generation   uint64
}

func NewRenderer(backend Backend, icons IconSet, intents chan<- Intent) *Renderer {
	return &Renderer{backend: backend, icons: icons, intents: intents}
}

func (r *Renderer) Render(model MenuModel) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancelRender != nil {
		r.cancelRender()
	}
	r.generation++
	generation := r.generation
	ctx, cancel := context.WithCancel(context.Background())
	r.cancelRender = cancel

	if icon := r.icons[model.Icon]; len(icon) > 0 {
		r.backend.SetIcon(icon)
	}
	r.backend.SetTitle(model.Title)
	r.backend.SetTooltip(model.Tooltip)
	r.backend.ResetMenu()
	for _, item := range model.Items {
		r.addItem(ctx, generation, nil, item)
	}
}

func (r *Renderer) Close() {
	r.mu.Lock()
	// Invalidate listeners even when a click races with cancellation.
	r.generation++
	if r.cancelRender != nil {
		r.cancelRender()
		r.cancelRender = nil
	}
	r.mu.Unlock()
}

func (r *Renderer) addItem(ctx context.Context, generation uint64, parent ItemHandle, model MenuItemModel) {
	if model.Separator {
		if parent == nil {
			r.backend.AddSeparator()
		} else {
			parent.AddSeparator()
		}
		return
	}

	var item ItemHandle
	if parent == nil {
		item = r.backend.AddMenuItem(model.Title, model.Tooltip)
	} else {
		item = parent.AddSubMenuItem(model.Title, model.Tooltip)
	}
	if !model.Enabled {
		item.Disable()
	}
	for _, child := range model.Children {
		r.addItem(ctx, generation, item, child)
	}
	if model.Intent != nil && model.Enabled {
		intent := *model.Intent
		go r.forwardClicks(ctx, generation, item.Clicked(), intent)
	}
}

func (r *Renderer) forwardClicks(ctx context.Context, generation uint64, clicks <-chan struct{}, intent Intent) {
	for {
		select {
		case _, ok := <-clicks:
			if !ok {
				return
			}
			// Cancellation alone is insufficient when clicks and ctx.Done are
			// ready together: select may choose either. The generation check is
			// the authoritative stale-menu fence.
			r.mu.Lock()
			if ctx.Err() != nil || r.generation != generation {
				r.mu.Unlock()
				return
			}
			// Keep the generation check and enqueue atomic with respect to
			// Render/Close. Delivery is deliberately non-blocking: a stalled
			// consumer must not freeze menu rebuild or application shutdown.
			select {
			case r.intents <- intent:
			default:
			}
			r.mu.Unlock()
		case <-ctx.Done():
			return
		}
	}
}
