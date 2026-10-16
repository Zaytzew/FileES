package v1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"filees/pkg/clientview"
	"github.com/google/uuid"
)

const BatchSchema = "filees.reservation-batch/v1"
const BatchSize = 4
const BatchMaxRepositories = 4096
const BatchMaxRequestBytes = 1 << 20
const BatchMaxResponseBytes = 32 << 20

// Scope binds pagination and content hashes to authority, not its heartbeat.
func BatchScope(serverID string, view clientview.View) string {
	view.GeneratedAt = time.Time{}
	return batchDigest(struct {
		Server string
		View   clientview.View
	}{serverID, view})
}

type BatchRequest struct {
	Schema string            `json:"schema"`
	Scope  string            `json:"scope"`
	After  string            `json:"after,omitempty"`
	Known  map[string]string `json:"known,omitempty"`
}

type BatchEntry struct {
	RepoID          string        `json:"repo_id"`
	Version         string        `json:"version"`
	Result          *Result       `json:"result,omitempty"`
	AsOf            time.Time     `json:"as_of"`
	Generation      string        `json:"generation"`
	ViewGeneratedAt *time.Time    `json:"view_generated_at"`
	StorageWrite    *StorageWrite `json:"storage_write,omitempty"`
}

type BatchResponse struct {
	Schema       string       `json:"schema"`
	ServerID     string       `json:"server_id"`
	ClientID     string       `json:"client_id"`
	RealmID      string       `json:"realm_id"`
	Scope        string       `json:"scope"`
	After        string       `json:"after"`
	Next         string       `json:"next"`
	Repositories []string     `json:"repositories"`
	Entries      []BatchEntry `json:"entries"`
}

func batchDigest(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func batchID(id string) bool { u, err := uuid.Parse(id); return err == nil && u.String() == id }
func batchHash(h string) bool {
	b, err := hex.DecodeString(h)
	return err == nil && len(b) == 32 && hex.EncodeToString(b) == h
}

func (r BatchRequest) Validate() error {
	if r.Schema != BatchSchema || !batchHash(r.Scope) || (r.After != "" && !batchID(r.After)) || len(r.Known) > BatchMaxRepositories {
		return errors.New("invalid reservation batch request")
	}
	for id, version := range r.Known {
		if !batchID(id) || !batchHash(version) {
			return errors.New("invalid reservation batch known version")
		}
	}
	return nil
}

func ParseBatchRequest(raw []byte) (BatchRequest, error) {
	var r BatchRequest
	if len(raw) > BatchMaxRequestBytes {
		return r, errors.New("reservation batch request exceeds limit")
	}
	if err := decodeExactlyOne(raw, &r); err != nil {
		return r, err
	}
	return r, r.Validate()
}

// BatchPage requires a complete sorted inventory, with no skipped/duplicate IDs.
func BatchPage(ids []string, after string) ([]string, string, error) {
	if len(ids) > BatchMaxRepositories {
		return nil, "", errors.New("reservation inventory exceeds limit")
	}
	start := 0
	found := after == ""
	for i, id := range ids {
		if !batchID(id) || (i > 0 && ids[i-1] >= id) {
			return nil, "", errors.New("invalid reservation inventory")
		}
		if id == after {
			start, found = i+1, true
		}
	}
	if !found || (after != "" && start == len(ids)) {
		return nil, "", errors.New("invalid reservation cursor")
	}
	end := min(start+BatchSize, len(ids))
	next := ""
	if end < len(ids) {
		next = ids[end-1]
	}
	return ids[start:end], next, nil
}

// BatchVersion omits only observational fields, never locks, incarnation,
// revision, ownership, errors or authority. Sorting is not done here: producer
// ordering is canonical; a reordered payload safely becomes a full update.
func BatchVersion(scope string, r Result) string {
	r.AsOf, r.Generation, r.ViewGeneratedAt, r.StorageWrite = time.Time{}, "", nil, nil
	return batchDigest(struct {
		Scope  string
		Result Result
	}{scope, r})
}

func NewBatchEntry(scope string, r Result, known string) BatchEntry {
	e := BatchEntry{RepoID: r.RepoID, Version: BatchVersion(scope, r), AsOf: r.AsOf, Generation: r.Generation, ViewGeneratedAt: r.ViewGeneratedAt, StorageWrite: r.StorageWrite}
	if e.Version != known {
		e.Result = &r
	}
	return e
}

func ParseBatchResponse(raw []byte) (BatchResponse, error) {
	var r BatchResponse
	if len(raw) > BatchMaxResponseBytes {
		return r, errors.New("reservation batch response exceeds limit")
	}
	if err := decodeExactlyOne(raw, &r); err != nil {
		return r, err
	}
	return r, r.validateShape() // request/cache validation is atomic in ApplyBatch
}

func (r BatchResponse) validateShape() error {
	page, next, err := BatchPage(r.Repositories, r.After)
	if err != nil || r.Repositories == nil || r.Entries == nil || r.Schema != BatchSchema || !batchHash(r.Scope) || r.ServerID == "" || !batchID(r.ClientID) || !batchID(r.RealmID) || next != r.Next || len(page) != len(r.Entries) {
		return errors.New("invalid reservation batch response")
	}
	return nil
}

// ApplyBatch is all-or-nothing for a page. Receipt time belongs to the caller:
// use local request START, not response arrival or completion of all pages.
func ApplyBatch(req BatchRequest, r BatchResponse, serverID, clientID, realmID string, cached map[string]Result) (map[string]Result, error) {
	fail := errors.New("reservation batch does not match request or cache")
	if req.Validate() != nil || r.validateShape() != nil || r.Scope != req.Scope || r.After != req.After || r.ServerID != serverID || r.ClientID != clientID || r.RealmID != realmID {
		return nil, fail
	}
	ids, next, err := BatchPage(r.Repositories, req.After)
	if err != nil || next != r.Next || len(ids) != len(r.Entries) {
		return nil, fail
	}
	results := make(map[string]Result, len(ids))
	for i, e := range r.Entries {
		if e.RepoID != ids[i] || !batchHash(e.Version) {
			return nil, fail
		}
		var result Result
		if e.Result != nil {
			result = *e.Result
		} else {
			var ok bool
			result, ok = cached[e.RepoID]
			if !ok || req.Known[e.RepoID] != e.Version {
				return nil, fail
			}
		}
		if result.RepoID != e.RepoID || result.Schema != AutolockSchema || result.RepositoryState != "active" || BatchVersion(req.Scope, result) != e.Version {
			return nil, fail
		}
		result.AsOf, result.Generation, result.ViewGeneratedAt, result.StorageWrite = e.AsOf, e.Generation, e.ViewGeneratedAt, e.StorageWrite
		raw, err := json.Marshal(result)
		if err != nil {
			return nil, err
		}
		if _, err = ParseResult(raw); err != nil {
			return nil, err
		}
		results[e.RepoID] = result
	}
	return results, nil
}

func BatchInventory(view clientview.View) []string {
	ids := []string{}
	for _, repo := range view.Repositories {
		if repo.State == "active" {
			ids = append(ids, repo.RepoID)
		}
	}
	sort.Strings(ids)
	return ids
}
