// Package publicstate defines the read-only, server-scoped public-link mirror.
// Versions describe canonical projected content, not the number of reads.
// A mirror is presentation knowledge and never authorizes a mutation or visit.
package publicstate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sort"

	control "filees/pkg/control/v1"
	"github.com/google/uuid"
)

const Schema = "filees.public-links-state/v1"
const MaxRequestBytes = 1 << 20
const MaxResponseBytes = 32 << 20
const MaxItems = 4096

type Binding struct {
	ServerID string `json:"server_id"`
	ClientID string `json:"client_id"`
	RealmID  string `json:"realm_id"`
}

type Item struct {
	RepoDisplayName string                     `json:"repo_display_name"`
	Share           control.PublicShareSummary `json:"share"`
}

type Request struct {
	Schema string            `json:"schema"`
	Known  map[string]string `json:"known,omitempty"`
}

// Versions is the complete authorized inventory, including a verified empty
// inventory. An omitted old ID is removed. Changes carries only changed items.
type Response struct {
	Schema   string            `json:"schema"`
	Binding  Binding           `json:"binding"`
	Base     string            `json:"base"`
	Complete bool              `json:"complete"`
	Versions map[string]string `json:"versions"`
	Changes  []Item            `json:"changes"`
}

type Snapshot struct {
	Binding Binding
	Items   map[string]Item
}

func digest(v any) string {
	raw, _ := json.Marshal(v) // protocol values have no unsupported JSON types
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func itemVersion(binding Binding, item Item) string {
	return digest(struct {
		Binding Binding
		Item    Item
	}{binding, item})
}

func requestBase(req Request) string {
	known := req.Known
	if known == nil {
		known = map[string]string{}
	}
	return digest(known)
}

func validID(id string) bool {
	u, err := uuid.Parse(id)
	return err == nil && u.String() == id
}

func validVersions(v map[string]string) bool {
	if len(v) > MaxItems {
		return false
	}
	for id, version := range v {
		if !validID(id) || len(version) != 64 {
			return false
		}
		if _, err := hex.DecodeString(version); err != nil {
			return false
		}
	}
	return true
}

func (r Request) Validate() error {
	if r.Schema != Schema || !validVersions(r.Known) {
		return errors.New("invalid public link state request")
	}
	return nil
}

func decode(raw []byte, limit int, target any) error {
	if len(raw) > limit {
		return errors.New("public link state size limit")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing public link state data")
	}
	return nil
}

func ParseRequest(raw []byte) (Request, error) {
	var r Request
	if err := decode(raw, MaxRequestBytes, &r); err != nil {
		return r, err
	}
	return r, r.Validate()
}

func ParseResponse(raw []byte) (Response, error) {
	var r Response
	if err := decode(raw, MaxResponseBytes, &r); err != nil {
		return r, err
	}
	if r.Schema != Schema || !r.Complete || r.Versions == nil || !validVersions(r.Versions) || len(r.Changes) > MaxItems || r.Binding.ServerID == "" || !validID(r.Binding.ClientID) || !validID(r.Binding.RealmID) {
		return r, errors.New("invalid public link state response")
	}
	return r, nil
}

func (s Snapshot) Request() Request {
	r := Request{Schema: Schema, Known: map[string]string{}}
	for id, item := range s.Items {
		r.Known[id] = itemVersion(s.Binding, item)
	}
	return r
}

// Build reads an already-authorized collection. The caller owns authorization
// and must not call it with stale artifacts or an incomplete collection.
func Build(binding Binding, req Request, items []Item) (Response, error) {
	if err := req.Validate(); err != nil {
		return Response{}, err
	}
	r := Response{Schema: Schema, Binding: binding, Base: requestBase(req), Complete: true, Versions: map[string]string{}, Changes: []Item{}}
	if len(items) > MaxItems {
		return r, errors.New("too many public links")
	}
	for _, item := range items {
		id := item.Share.ChannelID
		if !validID(id) || !validID(item.Share.RepoID) {
			return r, errors.New("invalid public link identity")
		}
		if _, duplicate := r.Versions[id]; duplicate {
			return r, errors.New("duplicate public link identity")
		}
		version := itemVersion(binding, item)
		r.Versions[id] = version
		if req.Known[id] != version {
			r.Changes = append(r.Changes, item)
		}
	}
	sort.Slice(r.Changes, func(i, j int) bool { return r.Changes[i].Share.ChannelID < r.Changes[j].Share.ChannelID })
	return r, nil
}

// Apply is all-or-nothing. The previous snapshot remains untouched on any
// failure. Recompute versions from the actual bytes, never trust a naked ACK.
func (s Snapshot) Apply(req Request, r Response, expected Binding) (Snapshot, error) {
	if err := req.Validate(); err != nil {
		return Snapshot{}, err
	}
	if r.Schema != Schema || !r.Complete || r.Binding != expected || r.Base != requestBase(req) || r.Versions == nil || !validVersions(r.Versions) || len(r.Changes) > MaxItems {
		return Snapshot{}, errors.New("public link delta binding or base mismatch")
	}
	changes := map[string]Item{}
	for _, item := range r.Changes {
		id := item.Share.ChannelID
		if _, duplicate := changes[id]; duplicate {
			return Snapshot{}, errors.New("duplicate public link change")
		}
		if !validID(item.Share.RepoID) || r.Versions[id] != itemVersion(expected, item) {
			return Snapshot{}, errors.New("public link delta content mismatch")
		}
		changes[id] = item
	}
	next := Snapshot{Binding: expected, Items: map[string]Item{}}
	for id, version := range r.Versions {
		item, changed := changes[id]
		if !changed {
			var exists bool
			item, exists = s.Items[id]
			if !exists || s.Binding != expected || req.Known[id] != version || itemVersion(expected, item) != version {
				return Snapshot{}, errors.New("public link ACK has no matching cached item")
			}
		}
		next.Items[id] = item
	}
	return next, nil
}
