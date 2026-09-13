package androidbind

import (
	"testing"

	"filees/internal/domaincatalog"
	"filees/pkg/errcat"
)

// TestExplainKeepsTheSentencesItAlwaysReturned pins the whole path, not the
// string table: raw text → errmap.Classify → key → language pack → renderer.
//
// The sentences below were captured from the previous implementation, which
// read errcat.Spec.Polish directly, before that source was replaced. Exporting
// Spec.Polish into the pack does not by itself prove that the classification
// and the rendering in between still produce the same result, which is why
// each case starts from the raw error a device would actually report.
//
// Android is a released client. If a change makes one of these differ, that is
// a change to what a user reads, and it needs a decision — not a test update.
func TestExplainKeepsTheSentencesItAlwaysReturned(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "payload corrupt",
			raw:  "tree.payload_corrupt: sha256 or size mismatch",
			want: "Paczka uszkodziła się w transporcie (sha256 nie zgadza się z nagłówkiem). Nic nie zapisano — wyślij folder jeszcze raz.",
		},
		{
			name: "not a pack",
			raw:  "not a filees tree pack",
			want: "To zwykły plik ZIP, nie paczka FileES. Taki artefakt idzie jako jeden obiekt, nie jako drzewo.",
		},
		{
			name: "tree not ingested",
			raw:  "UPLOAD_TREE not ingested by worker",
			want: "Telefon spakował folder i wysłał jednym połączeniem. Serwer paczki jeszcze nie przyjmuje — brakuje apply filees-mobile-v1.",
		},
		{
			name: "operation not on server",
			raw:  "worker exited status 70",
			want: "Serwer nie zna tej operacji mobilnej. Zwykle stary filees-mobile-v1 (status 70) — brakuje podpisanego apply.",
		},
		{
			name: "session ended",
			raw:  "FILEES-SESSION-ENDED lease revoked",
			want: "Serwer zakończył tę sesję — spróbuj ponownie za chwilę",
		},
		{
			name: "connection dropped",
			raw:  "Timeout, server 10.0.0.1 not responding.",
			want: "Połączenie zostało przerwane w trakcie operacji",
		},
		{
			name: "network unreachable",
			raw:  "svn: E170013: Unable to connect to a repository",
			want: "Brak połączenia z siecią",
		},
		{
			name: "authentication failed",
			raw:  "svn: E170001: Authorization failed",
			want: "Uwierzytelnienie nie powiodło się",
		},
		{
			name: "working copy busy",
			raw:  "svn: E155004: Working copy locked; run svn cleanup",
			want: "Kopia robocza jest chwilowo zajęta przez inny lokalny proces",
		},
		{
			// This key carries a ladder now. Classify has no structured
			// details, so it must resolve to the standalone rung — the same
			// sentence Explain has always returned, with no holes in it.
			name: "file locked by somebody else",
			raw:  "svn: E200015: file is already locked by anna",
			want: "Plik jest w tej chwili wypożyczony przez kogoś innego",
		},
		{
			name: "working copy out of date",
			raw:  "svn: E160028: out of date",
			want: "Kopia robocza jest nieaktualna — najpierw pobierz zmiany",
		},
		{
			name: "not under version control",
			raw:  "svn: E200009: is not under version control",
			want: "Ścieżka nie jest pod kontrolą wersji",
		},
		{
			name: "commit failed",
			raw:  "commit failed: pre-commit hook",
			want: "Zapis na serwer nie powiódł się",
		},
		{
			// Unclassified text keeps returning nothing, so the Kotlin side
			// keeps its own local fallback instead of being handed a guess.
			name: "unclassified",
			raw:  "całkiem inny błąd bez igły",
			want: "",
		},
		{name: "empty", raw: "", want: ""},
		{name: "whitespace only", raw: "   \t ", want: ""},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := Explain(testCase.raw); got != testCase.want {
				t.Errorf("Explain(%q)\n got  %q\n want %q", testCase.raw, got, testCase.want)
			}
		})
	}
}

func TestExplainInUsesTheRequestedLanguagePack(t *testing.T) {
	raw := "E170001: Authorization failed"
	pl := ExplainIn(raw, "pl")
	en := ExplainIn(raw, "en")
	if pl == "" || en == "" {
		t.Fatalf("empty sentence pl=%q en=%q", pl, en)
	}
	if pl == en {
		t.Fatalf("pl and en should differ: %q", pl)
	}
	if ExplainIn(raw, "pt-BR") != pl {
		t.Fatalf("unknown locale should keep Polish")
	}
}

// Whatever errmap can classify must have a sentence, or a device shows
// nothing for a failure the dictionary already understands.
func TestEveryClassifiableKeyHasASentence(t *testing.T) {
	registry, err := domaincatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	pack, ok := registry.Pack(explainLocale)
	if !ok {
		t.Fatalf("no %q pack", explainLocale)
	}
	classifiable := []errcat.Key{
		errcat.KeyMobileTreeCorrupt, errcat.KeyMobileTreeNotAPack,
		errcat.KeyMobileTreeNotIngested, errcat.KeyMobileOpNotOnServer,
		errcat.KeySessionEnded, errcat.KeyConnectionDropped,
		errcat.KeyNetUnreachable, errcat.KeyAuthFailed,
		errcat.KeyWorkingCopyBusy, errcat.KeyLockHeldByOther,
		errcat.KeyCommitOutdated, errcat.KeyCommitNoVCS, errcat.KeyCommitFailed,
	}
	for _, key := range classifiable {
		if _, ok := pack.Messages[string(key)]; !ok {
			t.Errorf("%q has no sentence in the %s pack", key, explainLocale)
		}
	}
}

// Explain is a presentation surface. The mobile envelope keeps carrying the
// English diagnostic, and nothing here may quietly change that.
func TestExplainDoesNotChangeTheWireDiagnostic(t *testing.T) {
	spec, ok := errcat.ByKey(errcat.KeyNetUnreachable)
	if !ok {
		t.Fatal("dictionary lost net.unreachable")
	}
	if spec.Diagnostic == "" {
		t.Fatal("the wire diagnostic must stay English and present")
	}
	if got := Explain("svn: E170013: Unable to connect to a repository"); got == spec.Diagnostic {
		t.Fatal("Explain returned the wire diagnostic instead of the reader's sentence")
	}
}
