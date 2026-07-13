package tray

import (
	"sync"
	"testing"
	"time"

	app "filees/internal/gui/app"
)

type fakeBackend struct {
	mu       sync.Mutex
	icon     []byte
	title    string
	tooltip  string
	resets   int
	items    []*fakeItem
	quitCall int
}

type fakeItem struct {
	mu       sync.Mutex
	title    string
	disabled bool
	clicks   chan struct{}
	children []*fakeItem
	seps     int
}

func (b *fakeBackend) Run(onReady, onExit func()) { onReady(); onExit() }
func (b *fakeBackend) Quit()                      { b.mu.Lock(); b.quitCall++; b.mu.Unlock() }
func (b *fakeBackend) ResetMenu()                 { b.mu.Lock(); b.resets++; b.items = nil; b.mu.Unlock() }
func (b *fakeBackend) SetIcon(icon []byte) {
	b.mu.Lock()
	b.icon = append([]byte(nil), icon...)
	b.mu.Unlock()
}
func (b *fakeBackend) SetTitle(title string) { b.mu.Lock(); b.title = title; b.mu.Unlock() }
func (b *fakeBackend) SetTooltip(tip string) { b.mu.Lock(); b.tooltip = tip; b.mu.Unlock() }
func (b *fakeBackend) AddSeparator()         {}
func (b *fakeBackend) AddMenuItem(title, _ string) ItemHandle {
	item := newFakeItem(title)
	b.mu.Lock()
	b.items = append(b.items, item)
	b.mu.Unlock()
	return item
}

func newFakeItem(title string) *fakeItem {
	return &fakeItem{title: title, clicks: make(chan struct{}, 8)}
}

func (i *fakeItem) AddSubMenuItem(title, _ string) ItemHandle {
	child := newFakeItem(title)
	i.mu.Lock()
	i.children = append(i.children, child)
	i.mu.Unlock()
	return child
}
func (i *fakeItem) AddSeparator()            { i.mu.Lock(); i.seps++; i.mu.Unlock() }
func (i *fakeItem) Disable()                 { i.mu.Lock(); i.disabled = true; i.mu.Unlock() }
func (i *fakeItem) Clicked() <-chan struct{} { return i.clicks }

func TestRendererAppliesCompleteModel(t *testing.T) {
	backend := &fakeBackend{}
	intents := make(chan Intent, 4)
	renderer := NewRenderer(backend, IconSet{app.IconActive: []byte("active-icon")}, intents)
	defer renderer.Close()

	renderer.Render(MenuModel{
		Icon: app.IconActive, Title: "FileES", Tooltip: "ready",
		Items: []MenuItemModel{
			disabledItem("status", "Połączono"),
			{ID: "repo", Title: "projectA", Enabled: true, Children: []MenuItemModel{
				actionItem("open", "Otwórz", "", Intent{Kind: IntentOpenFolder, RepoID: "projectA"}),
			}},
		},
	})

	backend.mu.Lock()
	if string(backend.icon) != "active-icon" || backend.title != "FileES" || backend.tooltip != "ready" || backend.resets != 1 {
		t.Fatalf("backend state = %#v", backend)
	}
	items := append([]*fakeItem(nil), backend.items...)
	backend.mu.Unlock()
	if len(items) != 2 {
		t.Fatalf("top-level items = %d", len(items))
	}
	items[0].mu.Lock()
	disabled := items[0].disabled
	items[0].mu.Unlock()
	if !disabled {
		t.Fatal("status item should be disabled")
	}
	items[1].mu.Lock()
	open := items[1].children[0]
	items[1].mu.Unlock()
	open.clicks <- struct{}{}
	select {
	case intent := <-intents:
		if intent.Kind != IntentOpenFolder || intent.RepoID != "projectA" {
			t.Fatalf("intent = %#v", intent)
		}
	case <-time.After(time.Second):
		t.Fatal("click intent not forwarded")
	}
}

func TestRendererCancelsListenersFromPreviousGeneration(t *testing.T) {
	backend := &fakeBackend{}
	intents := make(chan Intent, 4)
	renderer := NewRenderer(backend, nil, intents)
	defer renderer.Close()

	renderer.Render(MenuModel{Items: []MenuItemModel{
		actionItem("old", "Old", "", Intent{Kind: IntentReconnect}),
	}})
	backend.mu.Lock()
	old := backend.items[0]
	backend.mu.Unlock()

	renderer.Render(MenuModel{Items: []MenuItemModel{
		actionItem("new", "New", "", Intent{Kind: IntentQuit}),
	}})
	old.clicks <- struct{}{}
	select {
	case intent := <-intents:
		t.Fatalf("old menu emitted intent after rebuild: %#v", intent)
	case <-time.After(100 * time.Millisecond):
	}

	backend.mu.Lock()
	current := backend.items[0]
	backend.mu.Unlock()
	current.clicks <- struct{}{}
	select {
	case intent := <-intents:
		if intent.Kind != IntentQuit {
			t.Fatalf("intent = %#v", intent)
		}
	case <-time.After(time.Second):
		t.Fatal("current menu click not forwarded")
	}
}
