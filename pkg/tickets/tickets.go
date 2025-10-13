package tickets

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

// Maker to prosty interfejs – w razie czego możesz zamienić na mock w testach.
type Maker interface {
	CreateNotice(wc string, clientUUID, title, body string) (string, error)
}

type maker struct{}

func New() Maker { return &maker{} }

// CreateNotice zapisuje .filees/tickets/NOTICE-<uuid>.req (INI + [payload]) i zwraca ścieżkę.
func (m *maker) CreateNotice(wc string, clientUUID, title, body string) (string, error) {
	tdir := filepath.Join(wc, ".filees", "tickets")
	if err := os.MkdirAll(tdir, 0o755); err != nil {
		return "", err
	}
	id := uuid.NewString()
	fn := filepath.Join(tdir, "NOTICE-"+id+".req")

	f, err := os.Create(fn)
	if err != nil {
		return "", err
	}
	defer f.Close()

	ts := time.Now().UTC().Format(time.RFC3339)
	// INI-lite, shell-friendly
	if _, err := fmt.Fprintf(f,
		"TYPE=NOTICE\nCLIENT=%s\nTS=%s\nID=%s\n\n[payload]\nTITLE=%s\n%s\n",
		clientUUID, ts, id, sanitizeLine(title), body); err != nil {
		return "", err
	}
	return fn, nil
}

// pojedynczą linię tytułu czyścimy z CR/LF
func sanitizeLine(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '\r' || r == '\n' {
			continue
		}
		out = append(out, r)
	}
	return string(out)
}
