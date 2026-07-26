package repoworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"filees/pkg/clientview"
)

const RepositorySchema = "filees.repository/v1"

// repositoryRecordPath builds the canonical record path for repoID and refuses
// any identifier that would not stay inside the records directory. The wire
// contract already requires a UUID (pkg/control/v1 Ticket.Validate), but this
// is the last point before a client-influenced string becomes a filesystem
// path, and it is reached from several callers — so containment is enforced
// here rather than trusted from upstream. A repo_id is an opaque identifier,
// never a path: a separator or parent reference in one is always a bug or an
// attack, so both are rejected outright instead of being cleaned away.
func repositoryRecordPath(serviceWC, repoID string) (string, error) {
	if repoID == "" || repoID == "." || repoID == ".." ||
		strings.ContainsAny(repoID, `/\`) ||
		strings.ContainsRune(repoID, 0) {
		return "", fmt.Errorf("invalid repository id %q", repoID)
	}
	root := filepath.Join(serviceWC, "admin", "repositories")
	target := filepath.Join(root, repoID+".json")
	rel, err := filepath.Rel(root, target)
	if err != nil || rel != repoID+".json" {
		return "", fmt.Errorf("repository id %q escapes the records directory", repoID)
	}
	return target, nil
}

type repositoryRecord struct {
	Schema       string    `json:"schema"`
	RepoID       string    `json:"repo_id"`
	OwnerRealmID string    `json:"owner_realm_id"`
	DisplayName  string    `json:"display_name"`
	URL          string    `json:"url"`
	State        string    `json:"state"`
	CreatedAt    time.Time `json:"created_at"`
}

type PublishRunner interface {
	Publish(context.Context, []string, string) error
}

type ServicePublisher struct {
	ServiceWC, DataAuthzFile string
	Runner                   PublishRunner
	Now                      func() time.Time
}

func (p ServicePublisher) Publish(ctx context.Context, repoID, realmID, name, url string) error {
	if !filepath.IsAbs(p.ServiceWC) || !filepath.IsAbs(p.DataAuthzFile) || p.Runner == nil {
		return errors.New("authority publisher is incomplete")
	}
	now := time.Now().UTC()
	if p.Now != nil {
		now = p.Now().UTC()
	}
	repoPath, err := repositoryRecordPath(p.ServiceWC, repoID)
	if err != nil {
		return err
	}
	record := repositoryRecord{Schema: RepositorySchema, RepoID: repoID, OwnerRealmID: realmID, DisplayName: name, URL: url, State: "initializing", CreatedAt: now}
	if raw, err := os.ReadFile(repoPath); err == nil {
		var old repositoryRecord
		if json.Unmarshal(raw, &old) != nil || old.RepoID != repoID || old.OwnerRealmID != realmID || old.DisplayName != name || old.URL != url {
			return errors.New("canonical repository record conflicts")
		}
		record = old
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else if err := atomicJSON(repoPath, record); err != nil {
		return err
	}
	clientsRoot := filepath.Join(p.ServiceWC, "clients")
	entries, err := os.ReadDir(clientsRoot)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	changed := []string{repoPath}
	grantees := []string{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		viewPath := filepath.Join(clientsRoot, entry.Name(), "view.json")
		view, err := clientview.Load(viewPath)
		if err != nil {
			continue
		}
		if view.RealmID != realmID {
			continue
		}
		grantees = append(grantees, view.ClientID)
		found := false
		for _, r := range view.Repositories {
			if r.RepoID == repoID {
				if r.URL != url || r.OwnerRealmID != realmID || r.Access != "rw" {
					return errors.New("repository projection conflicts")
				}
				found = true
				break
			}
		}
		if found {
			continue
		}
		view.Generation++
		view.GeneratedAt = now
		view.Repositories = append(view.Repositories, clientview.Repository{RepoID: repoID, DisplayName: name, URL: url, Access: "rw", State: "initializing", OwnerRealmID: realmID, AttachmentPolicy: "optional"})
		sort.Slice(view.Repositories, func(i, j int) bool { return view.Repositories[i].RepoID < view.Repositories[j].RepoID })
		if _, err := clientview.StoreIfNewer(viewPath, view); err != nil {
			return err
		}
		changed = append(changed, viewPath)
	}
	_ = grantees
	authz, err := p.renderAuthz()
	if err != nil {
		return err
	}
	if err := atomicBytes(p.DataAuthzFile, authz); err != nil {
		return err
	}
	changed = append(changed, p.DataAuthzFile)
	return p.Runner.Publish(ctx, changed, "filees: create repository "+repoID)
}

func (p ServicePublisher) Activate(ctx context.Context, repoID, realmID string) error {
	if !filepath.IsAbs(p.ServiceWC) || p.Runner == nil {
		return errors.New("authority publisher is incomplete")
	}
	repoPath, err := repositoryRecordPath(p.ServiceWC, repoID)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(repoPath)
	if err != nil {
		return err
	}
	var record repositoryRecord
	if err := json.Unmarshal(raw, &record); err != nil || record.Schema != RepositorySchema || record.RepoID != repoID {
		return errors.New("canonical repository record is invalid")
	}
	if record.OwnerRealmID != realmID {
		return errors.New("authenticated realm does not own repository")
	}
	if record.State != "initializing" && record.State != "active" {
		return fmt.Errorf("repository state %q cannot be activated", record.State)
	}
	now := time.Now().UTC()
	if p.Now != nil {
		now = p.Now().UTC()
	}
	changed := []string{}
	if record.State != "active" {
		record.State = "active"
		if err := atomicJSON(repoPath, record); err != nil {
			return err
		}
		changed = append(changed, repoPath)
	}
	entries, err := os.ReadDir(filepath.Join(p.ServiceWC, "clients"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		viewPath := filepath.Join(p.ServiceWC, "clients", entry.Name(), "view.json")
		view, err := clientview.Load(viewPath)
		if err != nil || view.RealmID != realmID {
			continue
		}
		updated := false
		for i := range view.Repositories {
			if view.Repositories[i].RepoID == repoID && view.Repositories[i].State == "initializing" {
				view.Repositories[i].State = "active"
				updated = true
			}
		}
		if !updated {
			continue
		}
		view.Generation++
		view.GeneratedAt = now
		if _, err := clientview.StoreIfNewer(viewPath, view); err != nil {
			return err
		}
		changed = append(changed, viewPath)
	}
	if len(changed) == 0 {
		return nil
	}
	return p.Runner.Publish(ctx, changed, "filees: activate repository "+repoID)
}

// TransferOwner reassigns a repository's owning realm - the administrative
// escape hatch for a repo orphaned by a whole-realm revoke
// (AUTOLOCK_CREATOR_OWNERSHIP_CONCEPT_V2.md §6). It rewrites the canonical
// record, drops the repository from every client view that belonged to the
// old owner realm (in this V1, single-owner-per-repo world there is nothing
// else to "zero" - no separate delegation store exists yet), adds/updates an
// entry for every client of the new owner realm, and regenerates authz so
// renderAuthz's owner-realm group picks up the change immediately.
func (p ServicePublisher) TransferOwner(ctx context.Context, repoID, newRealmID string) error {
	if !filepath.IsAbs(p.ServiceWC) || !filepath.IsAbs(p.DataAuthzFile) || p.Runner == nil {
		return errors.New("authority publisher is incomplete")
	}
	repoPath, err := repositoryRecordPath(p.ServiceWC, repoID)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(repoPath)
	if err != nil {
		return err
	}
	var record repositoryRecord
	if err := json.Unmarshal(raw, &record); err != nil || record.Schema != RepositorySchema || record.RepoID != repoID {
		return errors.New("canonical repository record is invalid")
	}
	oldRealmID := record.OwnerRealmID
	if oldRealmID == newRealmID {
		return nil // already owned by the requested realm
	}
	now := time.Now().UTC()
	if p.Now != nil {
		now = p.Now().UTC()
	}
	record.OwnerRealmID = newRealmID
	if err := atomicJSON(repoPath, record); err != nil {
		return err
	}
	changed := []string{repoPath}

	clientsRoot := filepath.Join(p.ServiceWC, "clients")
	entries, err := os.ReadDir(clientsRoot)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		viewPath := filepath.Join(clientsRoot, entry.Name(), "view.json")
		view, err := clientview.Load(viewPath)
		if err != nil {
			continue
		}
		switch view.RealmID {
		case oldRealmID:
			kept := view.Repositories[:0]
			removed := false
			for _, r := range view.Repositories {
				if r.RepoID == repoID {
					removed = true
					continue
				}
				kept = append(kept, r)
			}
			if !removed {
				continue
			}
			view.Repositories = kept
		case newRealmID:
			found := false
			for i := range view.Repositories {
				if view.Repositories[i].RepoID == repoID {
					view.Repositories[i].OwnerRealmID = newRealmID
					view.Repositories[i].Access = "rw"
					found = true
					break
				}
			}
			if !found {
				view.Repositories = append(view.Repositories, clientview.Repository{RepoID: repoID, DisplayName: record.DisplayName, URL: record.URL, Access: "rw", State: record.State, OwnerRealmID: newRealmID, AttachmentPolicy: "optional"})
				sort.Slice(view.Repositories, func(i, j int) bool { return view.Repositories[i].RepoID < view.Repositories[j].RepoID })
			}
		default:
			continue
		}
		view.Generation++
		view.GeneratedAt = now
		if _, err := clientview.StoreIfNewer(viewPath, view); err != nil {
			return err
		}
		changed = append(changed, viewPath)
	}

	authz, err := p.renderAuthz()
	if err != nil {
		return err
	}
	if err := atomicBytes(p.DataAuthzFile, authz); err != nil {
		return err
	}
	changed = append(changed, p.DataAuthzFile)
	return p.Runner.Publish(ctx, changed, "filees: transfer repository "+repoID+" owner")
}

func (p ServicePublisher) renderAuthz() ([]byte, error) {
	repoDir := filepath.Join(p.ServiceWC, "admin", "repositories")
	entries, err := os.ReadDir(repoDir)
	if err != nil {
		return nil, err
	}
	records := []repositoryRecord{}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(repoDir, e.Name()))
		if err != nil {
			return nil, err
		}
		var r repositoryRecord
		if err = json.Unmarshal(raw, &r); err != nil || r.Schema != RepositorySchema {
			return nil, errors.New("invalid canonical repository record")
		}
		records = append(records, r)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].RepoID < records[j].RepoID })
	views := []clientview.View{}
	clientEntries, _ := os.ReadDir(filepath.Join(p.ServiceWC, "clients"))
	for _, e := range clientEntries {
		if !e.IsDir() {
			continue
		}
		if v, err := clientview.Load(filepath.Join(p.ServiceWC, "clients", e.Name(), "view.json")); err == nil {
			views = append(views, v)
		}
	}
	var out strings.Builder
	fmt.Fprintln(&out, "[groups]")
	groups := map[string][]string{}
	for _, r := range records {
		for _, v := range views {
			if v.RealmID != r.OwnerRealmID {
				continue
			}
			for _, vr := range v.Repositories {
				if vr.RepoID == r.RepoID && vr.Access == "rw" {
					groups[r.RepoID] = append(groups[r.RepoID], v.ClientID)
				}
			}
		}
		sort.Strings(groups[r.RepoID])
		fmt.Fprintf(&out, "owner-%s = %s\n", r.RepoID, strings.Join(groups[r.RepoID], ","))
	}
	for _, r := range records {
		fmt.Fprintf(&out, "\n[%s:/]\n@owner-%s = rw\n* =\n", r.RepoID, r.RepoID)
	}
	return []byte(out.String()), nil
}

func atomicJSON(path string, value any) error {
	raw, e := json.MarshalIndent(value, "", "  ")
	if e != nil {
		return e
	}
	return atomicBytes(path, append(raw, '\n'))
}
func atomicBytes(path string, raw []byte) error {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".publish-*.tmp")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(raw)
	}
	if e == nil {
		e = f.Sync()
	}
	if ce := f.Close(); e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	if e = os.Rename(tmp, path); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
