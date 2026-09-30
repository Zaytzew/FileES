package releasenotes

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func valid() *Notes {
	return &Notes{Schema: Schema, ReleaseID: "r1720", Sequence: 1720, Component: "desktop", SVNRevision: "1720", HistoryFrom: 1698,
		Items: []Item{
			{Sequence: 1698, Scope: "desktop", PL: "Starsza nowość", EN: "Older feature"},
			{Sequence: 1720, Scope: "windows", Kind: KindFix, PL: "Poprawka widoczna dla użytkownika", EN: "A fix users notice"},
			{Sequence: 1720, Scope: "desktop", Kind: KindSecurity, PL: "Poprawki bezpieczeństwa", EN: "Security fixes"},
			{Sequence: 1720, Scope: "linux", PL: "Nowość", EN: "Feature"},
		}}
}

func encode(t *testing.T, n *Notes) []byte {
	t.Helper()
	data, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestParseAcceptsReviewedNotes(t *testing.T) {
	if _, err := Parse(encode(t, valid())); err != nil {
		t.Fatal(err)
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]func(*Notes){
		"schema":            func(n *Notes) { n.Schema = "other" },
		"missing english":   func(n *Notes) { n.Items[1].EN = "" },
		"bidi override":     func(n *Notes) { n.Items[1].PL = "abc" + string(rune(0x202E)) + "def" },
		"zero width":        func(n *Notes) { n.Items[1].PL = "abc" + string(rune(0x200B)) + "def" },
		"line break":        func(n *Notes) { n.Items[1].EN = "one\ntwo" },
		"padding":           func(n *Notes) { n.Items[1].EN = " padded" },
		"too long":          func(n *Notes) { n.Items[1].PL = strings.Repeat("ą", MaxTextRunes+1) },
		"foreign scope":     func(n *Notes) { n.Items[1].Scope = "server" },
		"admin on desktop":  func(n *Notes) { n.Items[1].Kind = KindAdmin },
		"unknown kind":      func(n *Notes) { n.Items[1].Kind = "breaking" },
		"future sequence":   func(n *Notes) { n.Items[1].Sequence = 1721 },
		"before history":    func(n *Notes) { n.Items[0].Sequence = 1600 },
		"duplicate":         func(n *Notes) { n.Items = append(n.Items, n.Items[3]) },
		"history after seq": func(n *Notes) { n.HistoryFrom = 1721 },
		"bad component":     func(n *Notes) { n.Component = "web" },
		"too many new": func(n *Notes) {
			for i := 0; i <= MaxNewItems; i++ {
				n.Items = append(n.Items, Item{Sequence: 1720, Scope: "desktop", PL: fmt.Sprint("p", i), EN: fmt.Sprint("e", i)})
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			n := valid()
			mutate(n)
			if _, err := Parse(encode(t, n)); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	if _, err := Parse([]byte(`{"schema":"filees.release-notes/v1","extra":1}`)); err == nil {
		t.Fatal("unknown field accepted")
	}
}

func TestParseDraftAcceptsOneLanguage(t *testing.T) {
	n := valid()
	n.Items[1].EN = ""
	if _, err := ParseDraft(encode(t, n)); err != nil {
		t.Fatal(err)
	}
}

func TestSinceOrdersAndFilters(t *testing.T) {
	n := valid()
	got := n.Since(1697, "desktop", "windows")
	if len(got.Items) != 3 || got.Incomplete {
		t.Fatalf("%+v", got)
	}
	if got.Items[0].Kind != KindSecurity || got.Items[1].PL != "Starsza nowość" || got.Items[2].Kind != KindFix {
		t.Fatalf("order %+v", got.Items)
	}
	if got := n.Since(1698, "linux", "desktop"); len(got.Items) != 2 {
		t.Fatalf("since the previous publication: %+v", got)
	}
	if got := n.Since(0, "desktop"); len(got.Items) != 1 || got.Items[0].Sequence != 1720 {
		t.Fatalf("first publication shows only this release: %+v", got)
	}
	if got := n.Since(1500, "desktop"); !got.Incomplete {
		t.Fatal("a page older than the carried history is not told so")
	}
}

const logXML = `<?xml version="1.0" encoding="UTF-8"?>
<log>
<logentry revision="1710"><author>x</author><msg>Refactor.
Co nowego [desktop]: Pobieranie strumieniem
What's new [desktop]: Streamed downloads
Co nowego [server/admin]: Serwerowe
Co nowego [windows/fix]: Poprawka
</msg></logentry>
<logentry revision="1705"><msg>What’s new [linux]: Only English
Co nowego [phone]: Literówka
</msg></logentry>
</log>`

func TestDraftFromLog(t *testing.T) {
	entries, err := ParseSVNLog(strings.NewReader(logXML))
	if err != nil {
		t.Fatal(err)
	}
	if entries[0].Revision != 1705 {
		t.Fatal("log is not in revision order")
	}
	previous := valid()
	notes, warnings, err := Draft(previous, entries, "desktop", "r1730", 1730, "1730")
	if err != nil {
		t.Fatal(err)
	}
	var fresh []Item
	for _, item := range notes.Items {
		if item.Sequence == 1730 {
			fresh = append(fresh, item)
		}
	}
	if len(fresh) != 3 {
		t.Fatalf("fresh %+v", fresh)
	}
	if fresh[0] != (Item{Sequence: 1730, Scope: "linux", EN: "Only English"}) ||
		fresh[1] != (Item{Sequence: 1730, Scope: "desktop", PL: "Pobieranie strumieniem", EN: "Streamed downloads"}) ||
		fresh[2] != (Item{Sequence: 1730, Scope: "windows", Kind: KindFix, PL: "Poprawka"}) {
		t.Fatalf("fresh %+v", fresh)
	}
	if notes.HistoryFrom != 1698 || len(notes.Items) != len(previous.Items)+3 {
		t.Fatalf("carried %+v", notes)
	}
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, `"phone"`) || !strings.Contains(joined, "counterpart") {
		t.Fatalf("warnings %q", joined)
	}
	if _, err := Parse(encode(t, notes)); err == nil {
		t.Fatal("an incomplete draft passed the reviewed parser")
	}
	if _, err := ParseDraft(encode(t, notes)); err != nil {
		t.Fatal(err)
	}
}

func TestDraftDropsOldestReleasesFirst(t *testing.T) {
	previous := &Notes{Schema: Schema, ReleaseID: "r100", Sequence: 100, Component: "server", HistoryFrom: 1}
	for seq := uint64(1); seq <= 100; seq++ {
		if len(previous.Items) < MaxItems {
			previous.Items = append(previous.Items, Item{Sequence: seq, Scope: "server", PL: fmt.Sprint("p", seq), EN: fmt.Sprint("e", seq)})
		}
	}
	previous.Items = previous.Items[:MaxItems]
	previous.HistoryFrom = 1
	previous.Sequence = MaxItems
	entries := []LogEntry{{Revision: 5, Message: "Co nowego [server]: Nowe\nWhat's new [server]: New"}}
	notes, _, err := Draft(previous, entries, "server", "r200", 200, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(notes.Items) != MaxItems || notes.HistoryFrom != 2 || notes.Items[len(notes.Items)-1].Sequence != 200 {
		t.Fatalf("items %d history_from %d", len(notes.Items), notes.HistoryFrom)
	}
	if got := notes.Since(0, "server"); len(got.Items) != 1 {
		t.Fatalf("%+v", got)
	}
}

func TestDraftRefusesWrongPrevious(t *testing.T) {
	if _, _, err := Draft(valid(), nil, "server", "r9", 1800, ""); err == nil {
		t.Fatal("desktop notes carried into a server release")
	}
	if _, _, err := Draft(valid(), nil, "desktop", "r9", 1700, ""); err == nil {
		t.Fatal("newer previous notes accepted")
	}
}

func TestRaisedSecurityEpochNeedsOwnSecurityItem(t *testing.T) {
	n := valid()
	if err := n.CheckSecurityEpoch(1, 2); err != nil {
		t.Fatalf("own security item refused: %v", err)
	}
	if err := n.CheckSecurityEpoch(0, 2); err != nil || n.CheckSecurityEpoch(2, 2) != nil {
		t.Fatal("an unknown or unchanged epoch was checked")
	}
	n.Items[2].Kind = ""
	if err := n.CheckSecurityEpoch(1, 2); err == nil {
		t.Fatal("raised epoch without a security item accepted")
	}
	// A security item carried from an earlier release does not count.
	n.Items[0].Kind = KindSecurity
	if err := n.CheckSecurityEpoch(1, 2); err == nil {
		t.Fatal("a carried security item counted for this release")
	}
	if !n.AddSecurityPlaceholder(1, 2) || n.AddSecurityPlaceholder(1, 2) {
		t.Fatal("placeholder not added exactly once")
	}
	if _, err := ParseDraft(encode(t, n)); err != nil {
		t.Fatalf("draft with placeholder: %v", err)
	}
	if _, err := Parse(encode(t, n)); err == nil {
		t.Fatal("an empty security item passed review")
	}
	if n.AddSecurityPlaceholder(2, 2) {
		t.Fatal("placeholder added without a raised epoch")
	}
}
