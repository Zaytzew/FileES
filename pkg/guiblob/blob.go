// Package guiblob defines the opaque, realm-scoped GUI state envelope.
// Only the GUI understands Data; transports enforce size, identity and CAS.
package guiblob

import (
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"unicode/utf8"
)

const MaxBytes = 32768

type Write struct {
	Expected string `json:"expected"`
	Data     string `json:"data"`
}

type State struct {
	RealmID  string `json:"realm_id"`
	Version  string `json:"version"`
	Data     string `json:"data"`
	Conflict bool   `json:"conflict,omitempty"`
}

func (w Write) Validate() error {
	if len(w.Data) > MaxBytes || !utf8.ValidString(w.Data) {
		return errors.New("invalid GUI blob size or encoding")
	}
	raw, _ := json.Marshal(w)
	if len(raw) > 48<<10 {
		return errors.New("GUI blob exceeds transport envelope")
	}
	if w.Expected != "" {
		if _, err := uuid.Parse(w.Expected); err != nil {
			return errors.New("invalid GUI blob version")
		}
	}
	return nil
}

func (s State) Validate() error {
	if _, err := uuid.Parse(s.RealmID); err != nil {
		return errors.New("invalid GUI blob realm")
	}
	if s.Version == "" && s.Data != "" {
		return errors.New("unversioned GUI blob")
	}
	return (Write{Expected: s.Version, Data: s.Data}).Validate()
}
