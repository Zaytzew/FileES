package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filees/internal/releaseenvelope"
	"filees/internal/releasenotes"
)

func putNotes(t *testing.T, r *repo, key signer, notes releasenotes.Notes) {
	t.Helper()
	notes.Schema = releasenotes.Schema
	if notes.HistoryFrom == 0 {
		notes.HistoryFrom = 1
	}
	data, err := json.Marshal(notes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := releasenotes.Parse(data); err != nil {
		t.Fatal(err)
	}
	name := "releases/" + notes.ReleaseID + "/notes.json"
	r.files[name], r.files[name+".sig"] = data, key.sign(data)
}

const whatsNewTemplate = testTemplate + `
<section>{{DESKTOP_WHATS_NEW_PL}}</section><section>{{WINDOWS_AMD64_WHATS_NEW_PL}}</section><section>{{WINDOWS_AMD64_WHATS_NEW_EN}}</section>`

func TestCardsListWhatIsNewSinceThePreviousPublication(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "site"), 0o755); err != nil {
		t.Fatal(err)
	}
	key := newSigner(t)
	r := newRepo()
	r.release(t, key, "r1295", 1295, []byte("first"), "")
	p := publisher(t, r, key, root)
	p.Template = []byte(whatsNewTemplate)
	if _, err := p.Publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	page := mustRead(t, filepath.Join(p.OutDir, "index.html"))
	if strings.Contains(page, "whats-new") || strings.Contains(mustRead(t, filepath.Join(p.OutDir, "release.json")), "whats_new") {
		t.Fatalf("a release without notes grew a list:\n%s", page)
	}

	// Two releases later: the page skipped r1300, whose items the new file
	// still carries, and must show them together with r1310's own.
	r.release(t, key, "r1310", 1310, []byte("third"), "")
	putNotes(t, r, key, releasenotes.Notes{ReleaseID: "r1310", Sequence: 1310, Component: "desktop", HistoryFrom: 1290, Items: []releasenotes.Item{
		{Sequence: 1290, Scope: "desktop", PL: "Już opublikowane", EN: "Already published"},
		{Sequence: 1300, Scope: "desktop", PL: "Z pominiętego wydania", EN: "From the skipped release"},
		{Sequence: 1310, Scope: "windows", Kind: releasenotes.KindSecurity, PL: "Łatka <b>bezpieczeństwa</b>", EN: "Security patch"},
		{Sequence: 1310, Scope: "linux", PL: "Tylko Linux", EN: "Linux only"},
	}})
	if _, err := p.Publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	page = mustRead(t, filepath.Join(p.OutDir, "index.html"))
	for _, want := range []string{"Z pominiętego wydania", "Łatka &lt;b&gt;bezpieczeństwa&lt;/b&gt;", "<strong>Bezpieczeństwo:</strong>", "<strong>Security:</strong> Security patch"} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q:\n%s", want, page)
		}
	}
	for _, unwanted := range []string{"Już opublikowane", "Tylko Linux", "<b>"} {
		if strings.Contains(page, unwanted) {
			t.Errorf("page shows %q:\n%s", unwanted, page)
		}
	}
	var metadata struct {
		WhatsNew []metadataItem `json:"whats_new"`
	}
	if err := json.Unmarshal([]byte(mustRead(t, filepath.Join(p.OutDir, "release.json"))), &metadata); err != nil {
		t.Fatal(err)
	}
	if len(metadata.WhatsNew) != 2 || metadata.WhatsNew[0].PL != "Z pominiętego wydania" {
		t.Fatalf("release.json whats_new = %+v", metadata.WhatsNew)
	}
	// The next run of the same release keeps counting from r1295.
	result, err := p.Publish(context.Background())
	if err != nil || result.Changed {
		t.Fatalf("second run: %+v %v", result, err)
	}
	state, err := loadState(p.StatePath)
	if err != nil || state.NotesSince != 1295 {
		t.Fatalf("state %+v %v", state, err)
	}
}

func TestBadlySignedNotesKeepThePreviousPage(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "site"), 0o755); err != nil {
		t.Fatal(err)
	}
	key := newSigner(t)
	r := newRepo()
	r.release(t, key, "r1295", 1295, []byte("first"), "")
	p := publisher(t, r, key, root)
	p.Template = []byte(whatsNewTemplate)
	if _, err := p.Publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := mustRead(t, filepath.Join(p.OutDir, "index.html"))
	r.release(t, key, "r1300", 1300, []byte("second"), "")
	putNotes(t, r, key, releasenotes.Notes{ReleaseID: "r1300", Sequence: 1300, Component: "desktop", Items: []releasenotes.Item{
		{Sequence: 1300, Scope: "desktop", PL: "Nowość", EN: "Feature"},
	}})
	for name, mutate := range map[string]func(){
		"forged signature": func() {
			r.files["releases/r1300/notes.json.sig"] = newSigner(t).sign(r.files["releases/r1300/notes.json"])
		},
		"missing signature": func() { delete(r.files, "releases/r1300/notes.json.sig") },
		"other release": func() {
			putNotes(t, r, key, releasenotes.Notes{ReleaseID: "r1299", Sequence: 1299, Component: "desktop"})
			r.files["releases/r1300/notes.json"] = r.files["releases/r1299/notes.json"]
			r.files["releases/r1300/notes.json.sig"] = r.files["releases/r1299/notes.json.sig"]
		},
	} {
		saved := map[string][]byte{"releases/r1300/notes.json": r.files["releases/r1300/notes.json"], "releases/r1300/notes.json.sig": r.files["releases/r1300/notes.json.sig"]}
		mutate()
		if _, err := p.Publish(context.Background()); err == nil {
			t.Fatalf("%s: published", name)
		}
		if mustRead(t, filepath.Join(p.OutDir, "index.html")) != before {
			t.Fatalf("%s: the page changed", name)
		}
		for k, v := range saved {
			r.files[k] = v
		}
	}
}

func TestPromotedServerShowsEverythingSinceTheLastServerOnThePage(t *testing.T) {
	key := newSigner(t)
	r := newRepo()
	r.release(t, key, "r5", 5, []byte("MSI"), "")
	serverRelease(t, r, key, 1)
	p := publisher(t, r, key, t.TempDir())
	if err := os.MkdirAll(filepath.Dir(p.OutDir), 0o755); err != nil {
		t.Fatal(err)
	}
	p.Config.Server = &ServerConfig{Channel: "alpha", Platform: "openbsd-amd64"}
	p.Template = append([]byte(testTemplate), []byte(`<h2>{{SERVER_RELEASE_ID}}</h2><a href="{{SERVER_FILE}}">{{SERVER_SHA256}}</a><div>{{SERVER_WHATS_NEW_PL}}</div>`)...)
	if _, err := p.Publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	serverRelease(t, r, key, 1420)
	var items []releasenotes.Item
	for i := 0; i < 7; i++ {
		items = append(items, releasenotes.Item{Sequence: uint64(1400 + i), Scope: "server", PL: fmt.Sprint("Zmiana ", i), EN: fmt.Sprint("Change ", i)})
	}
	items = append(items, releasenotes.Item{Sequence: 1420, Scope: "server", Kind: releasenotes.KindAdmin, PL: "Uruchom migrację", EN: "Run the migration"})
	putNotes(t, r, key, releasenotes.Notes{ReleaseID: "r1420-server", Sequence: 1420, Component: "server", HistoryFrom: 1400, Items: items})
	if _, err := p.Publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	page := mustRead(t, filepath.Join(p.OutDir, "index.html"))
	for _, want := range []string{"<li class=\"admin\"><strong>Dla administratora:</strong> Uruchom migrację</li>", "<summary>Jeszcze 3 zmiany</summary>", "Zmiana 0", "Wcześniejsze zmiany nie mieszczą się"} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q:\n%s", want, page)
		}
	}
	if strings.Index(page, "Uruchom migrację") > strings.Index(page, "Zmiana 6") {
		t.Error("an admin item is not listed first")
	}
}

func TestAndroidWhatsNewIsVerifiedPlainData(t *testing.T) {
	key := newSigner(t)
	r := newRepo()
	putAndroidRelease(t, r, key, "r9", 9, []byte("apk"))
	out := t.TempDir()
	state := filepath.Join(t.TempDir(), "android-state.json")
	verifier := releaseenvelope.Ed25519Verifier{Keys: map[string][]byte{"release-test": key.public}}
	if _, err := publishAndroid(context.Background(), r, verifier, "release-test", out, state); err != nil {
		t.Fatal(err)
	}
	putAndroidRelease(t, r, key, "r12", 12, []byte("apk 12"))
	putNotes(t, r, key, releasenotes.Notes{ReleaseID: "r12", Sequence: 12, Component: "android", HistoryFrom: 10, Items: []releasenotes.Item{
		{Sequence: 10, Scope: "android", PL: "Z wydania 10", EN: "From release 10"},
		{Sequence: 12, Scope: "android", PL: "<script>", EN: "Twelve"},
	}})
	if changed, err := publishAndroid(context.Background(), r, verifier, "release-test", out, state); err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	var list struct {
		ReleaseID  string         `json:"release_id"`
		Items      []metadataItem `json:"items"`
		Incomplete bool           `json:"incomplete"`
	}
	if err := json.Unmarshal([]byte(mustRead(t, filepath.Join(out, androidWhatsNewFile))), &list); err != nil {
		t.Fatal(err)
	}
	if list.ReleaseID != "r12" || len(list.Items) != 2 || list.Items[0].PL != "<script>" || list.Incomplete {
		t.Fatalf("list %+v", list)
	}
	// A deployment upgraded under an unchanged release writes the list once.
	if err := os.Remove(filepath.Join(out, androidWhatsNewFile)); err != nil {
		t.Fatal(err)
	}
	if changed, err := publishAndroid(context.Background(), r, verifier, "release-test", out, state); err != nil || !changed {
		t.Fatalf("missing list not restored: changed=%v err=%v", changed, err)
	}
	r.files["releases/r12/notes.json.sig"] = []byte("forged")
	if _, err := publishAndroid(context.Background(), r, verifier, "release-test", out, state); err == nil {
		t.Fatal("forged android notes accepted")
	}
}

func TestPolishPluralOfMore(t *testing.T) {
	for n, want := range map[int]string{1: "Jeszcze 1 zmiana", 2: "Jeszcze 2 zmiany", 5: "Jeszcze 5 zmian", 12: "Jeszcze 12 zmian", 22: "Jeszcze 22 zmiany"} {
		if got := moreLabel(n, true); got != want {
			t.Errorf("%d: %q", n, got)
		}
	}
}
