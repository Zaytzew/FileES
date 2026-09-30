package main

import (
	"context"
	"fmt"
	"html"
	"strconv"
	"strings"

	"filees/internal/releaseenvelope"
	"filees/internal/releasenotes"
)

// cardItems is how many items a card lists before folding the rest.
const cardItems = 5

// maxMetadataItems is how many items release.json carries for the home page.
const maxMetadataItems = 5

// signedNotes reads releases/<id>/notes.json of one release and checks it
// against the release key and against the release it is shown next to. A
// release without notes is not an error: its cards simply have no list. A
// file that is there but unsigned, badly signed or about another release
// stops the publication, the same as a bad manifest does.
func signedNotes(ctx context.Context, fetcher releaseenvelope.Fetcher, verifier releaseenvelope.SignatureVerifier, keyID, releaseID string, sequence uint64, component string) (*releasenotes.Notes, error) {
	name := "releases/" + releaseID + "/" + releasenotes.FileName
	data, err := fetcher.Cat(ctx, name)
	if err != nil {
		if unpublished(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("fetch %s: %w", name, err)
	}
	signature, err := fetcher.Cat(ctx, name+".sig")
	if err != nil {
		return nil, fmt.Errorf("release notes %s have no signature: %w", name, err)
	}
	if verifier == nil {
		return nil, fmt.Errorf("release notes verifier is missing")
	}
	if err := verifier.Verify(ctx, keyID, data, signature); err != nil {
		return nil, fmt.Errorf("verify %s: %w", name, err)
	}
	notes, err := releasenotes.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if notes.ReleaseID != releaseID || notes.Sequence != sequence || notes.Component != component {
		return nil, fmt.Errorf("%s describes %s %s (sequence %d), not %s %s (sequence %d)",
			name, notes.Component, notes.ReleaseID, notes.Sequence, component, releaseID, sequence)
	}
	return notes, nil
}

// notesSince is the sequence a page counts "new" from: the release it
// published before the current one. It moves only when the release changes,
// so the list stays the same on every run until the next promotion.
func notesSince(previousRelease string, previousSequence, previousSince uint64, releaseID string) uint64 {
	switch {
	case previousRelease == "":
		return 0
	case previousRelease == releaseID:
		return previousSince
	default:
		return previousSequence
	}
}

// selection is the list of one card; nil notes select nothing.
func selection(notes *releasenotes.Notes, since uint64, scopes ...string) releasenotes.Selection {
	if notes == nil {
		return releasenotes.Selection{}
	}
	return notes.Since(since, scopes...)
}

// whatsNewHTML renders one card's list. It is built here, from escaped item
// text only, rather than taken from any file: the template decides where the
// list goes, the signed notes only what it says.
func whatsNewHTML(sel releasenotes.Selection, lang string) string {
	if len(sel.Items) == 0 {
		return ""
	}
	pl := lang == "pl"
	var out strings.Builder
	out.WriteString(`<div class="whats-new">`)
	if pl {
		out.WriteString(`<h3>Co nowego</h3>`)
	} else {
		out.WriteString(`<h3>What’s new</h3>`)
	}
	writeList := func(items []releasenotes.Item) {
		out.WriteString(`<ul>`)
		for _, item := range items {
			out.WriteString(`<li`)
			if item.Kind != "" && item.Kind != releasenotes.KindFeature {
				out.WriteString(` class="` + item.Kind + `"`)
			}
			out.WriteString(`>`)
			if label := kindLabel(item.Kind, pl); label != "" {
				out.WriteString(`<strong>` + label + `</strong> `)
			}
			text := item.EN
			if pl {
				text = item.PL
			}
			out.WriteString(html.EscapeString(text))
			out.WriteString(`</li>`)
		}
		out.WriteString(`</ul>`)
	}
	shown := sel.Items
	if len(shown) > cardItems {
		shown = sel.Items[:cardItems]
	}
	writeList(shown)
	if rest := sel.Items[len(shown):]; len(rest) > 0 {
		out.WriteString(`<details><summary>` + moreLabel(len(rest), pl) + `</summary>`)
		writeList(rest)
		out.WriteString(`</details>`)
	}
	if sel.Incomplete {
		if pl {
			out.WriteString(`<p class="whats-new-older">Wcześniejsze zmiany nie mieszczą się już na tej liście.</p>`)
		} else {
			out.WriteString(`<p class="whats-new-older">Earlier changes no longer fit this list.</p>`)
		}
	}
	out.WriteString(`</div>`)
	return out.String()
}

func kindLabel(kind string, pl bool) string {
	labels := map[string][2]string{
		releasenotes.KindSecurity: {"Bezpieczeństwo:", "Security:"},
		releasenotes.KindAdmin:    {"Dla administratora:", "For administrators:"},
		releasenotes.KindFix:      {"Poprawka:", "Fix:"},
	}
	label, ok := labels[kind]
	if !ok {
		return ""
	}
	if pl {
		return label[0]
	}
	return label[1]
}

// moreLabel is "3 more changes" with Polish plural forms.
func moreLabel(n int, pl bool) string {
	count := strconv.Itoa(n)
	if !pl {
		if n == 1 {
			return "1 more change"
		}
		return count + " more changes"
	}
	switch {
	case n == 1:
		return "Jeszcze 1 zmiana"
	case n%10 >= 2 && n%10 <= 4 && (n%100 < 12 || n%100 > 14):
		return "Jeszcze " + count + " zmiany"
	default:
		return "Jeszcze " + count + " zmian"
	}
}

// metadataItem is one entry of whats_new in release.json: plain text for
// release-badge.js and the Android card, which must insert it as text.
type metadataItem struct {
	Scope string `json:"scope"`
	Kind  string `json:"kind,omitempty"`
	PL    string `json:"pl"`
	EN    string `json:"en"`
}

func metadataItems(selections ...releasenotes.Selection) []metadataItem {
	return listItems(maxMetadataItems, selections...)
}

func listItems(limit int, selections ...releasenotes.Selection) []metadataItem {
	var out []metadataItem
	for _, sel := range selections {
		for _, item := range sel.Items {
			if len(out) == limit {
				return out
			}
			out = append(out, metadataItem{Scope: item.Scope, Kind: item.Kind, PL: item.PL, EN: item.EN})
		}
	}
	return out
}
