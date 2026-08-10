package repoworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"filees/pkg/clientview"
	"github.com/google/uuid"
)

const RealmGrantSchema = "filees.realm-grant/v1"

type RealmGrantRecord struct {
	Schema           string    `json:"schema"`
	RepoID           string    `json:"repo_id"`
	OwnerRealmID     string    `json:"owner_realm_id"`
	RecipientRealmID string    `json:"recipient_realm_id"`
	Access           string    `json:"access,omitempty"`
	State            string    `json:"state"`
	PathOwnerPolicy  string    `json:"path_owner_policy,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type RealmGrantRecipient struct{ RealmID, Alias, Access, State string }
type canonicalGrantClient struct{ RealmID, Kind string }

type RealmGrantAuthority interface {
	Grant(context.Context, string, string, string, string) (RealmGrantRecord, error)
	RevokeGrant(context.Context, string, string, string) (RealmGrantRecord, error)
	ListGrantRecipients(context.Context, string, string) ([]RealmGrantRecipient, error)
	SetRealmDirectoryVisibility(context.Context, string, string) (string, error)
}

func realmGrantPath(serviceWC, repoID, recipientRealmID string) (string, error) {
	if _, err := uuid.Parse(repoID); err != nil {
		return "", errors.New("realm grant repo_id must be UUID")
	}
	if _, err := uuid.Parse(recipientRealmID); err != nil {
		return "", errors.New("realm grant recipient_realm_id must be UUID")
	}
	return filepath.Join(serviceWC, "admin", "grants", repoID, recipientRealmID+".json"), nil
}

func (p ServicePublisher) Grant(ctx context.Context, ownerRealmID, recipientRealmID, repoID, access string) (RealmGrantRecord, error) {
	if err := p.validateGrantPublisher(); err != nil {
		return RealmGrantRecord{}, err
	}
	if access != "r" && access != "rw" {
		return RealmGrantRecord{}, errors.New("realm grant access must be r or rw")
	}
	if ownerRealmID == recipientRealmID {
		return RealmGrantRecord{}, errors.New("repository owner already has rw access")
	}
	repo, err := p.loadActiveRepository(repoID)
	if err != nil {
		return RealmGrantRecord{}, err
	}
	if repo.OwnerRealmID != ownerRealmID {
		return RealmGrantRecord{}, errors.New("authenticated realm does not own repository")
	}
	recipient, err := readRealmRecord(filepath.Join(p.ServiceWC, "admin", "realms", recipientRealmID+".json"))
	if err != nil || recipient.RealmID != recipientRealmID || recipient.State != "active" {
		return RealmGrantRecord{}, errors.New("recipient realm is not active")
	}
	path, err := realmGrantPath(p.ServiceWC, repoID, recipientRealmID)
	if err != nil {
		return RealmGrantRecord{}, err
	}
	now := p.now()
	record := RealmGrantRecord{Schema: RealmGrantSchema, RepoID: repoID, OwnerRealmID: ownerRealmID, RecipientRealmID: recipientRealmID, Access: access, State: "active", CreatedAt: now, UpdatedAt: now}
	if access == "rw" {
		record.PathOwnerPolicy = "first_committer"
	}
	if raw, readErr := os.ReadFile(path); readErr == nil {
		var old RealmGrantRecord
		if json.Unmarshal(raw, &old) != nil || validateRealmGrantRecord(old) != nil || old.RepoID != repoID || old.RecipientRealmID != recipientRealmID || old.OwnerRealmID != ownerRealmID {
			return RealmGrantRecord{}, errors.New("canonical realm grant conflicts")
		}
		record.CreatedAt = old.CreatedAt
		if old.State == record.State && old.Access == record.Access && old.PathOwnerPolicy == record.PathOwnerPolicy {
			return old, nil
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return RealmGrantRecord{}, readErr
	}
	if err := atomicJSON(path, record); err != nil {
		return RealmGrantRecord{}, err
	}
	changed, err := p.rebuildGrantAuthority()
	if err != nil {
		return RealmGrantRecord{}, err
	}
	changed = append([]string{path}, changed...)
	if err := p.Runner.Publish(ctx, changed, "filees: grant "+access+" "+repoID+" to realm "+recipientRealmID); err != nil {
		return RealmGrantRecord{}, err
	}
	return record, nil
}

func (p ServicePublisher) RevokeGrant(ctx context.Context, ownerRealmID, recipientRealmID, repoID string) (RealmGrantRecord, error) {
	if err := p.validateGrantPublisher(); err != nil {
		return RealmGrantRecord{}, err
	}
	repo, err := p.loadActiveRepository(repoID)
	if err != nil {
		return RealmGrantRecord{}, err
	}
	if repo.OwnerRealmID != ownerRealmID {
		return RealmGrantRecord{}, errors.New("authenticated realm does not own repository")
	}
	path, err := realmGrantPath(p.ServiceWC, repoID, recipientRealmID)
	if err != nil {
		return RealmGrantRecord{}, err
	}
	var record RealmGrantRecord
	raw, err := os.ReadFile(path)
	if err != nil {
		return RealmGrantRecord{}, err
	}
	if json.Unmarshal(raw, &record) != nil || validateRealmGrantRecord(record) != nil || record.OwnerRealmID != ownerRealmID || record.RecipientRealmID != recipientRealmID {
		return RealmGrantRecord{}, errors.New("canonical realm grant is invalid")
	}
	if record.State == "revoked" {
		return record, nil
	}
	record.State, record.Access, record.PathOwnerPolicy, record.UpdatedAt = "revoked", "", "", p.now()
	if err := atomicJSON(path, record); err != nil {
		return RealmGrantRecord{}, err
	}
	changed, err := p.rebuildGrantAuthority()
	if err != nil {
		return RealmGrantRecord{}, err
	}
	changed = append([]string{path}, changed...)
	if err := p.Runner.Publish(ctx, changed, "filees: revoke grant "+repoID+" from realm "+recipientRealmID); err != nil {
		return RealmGrantRecord{}, err
	}
	return record, nil
}

func (p ServicePublisher) ListGrantRecipients(_ context.Context, requesterRealmID, repoID string) ([]RealmGrantRecipient, error) {
	if !filepath.IsAbs(p.ServiceWC) {
		return nil, errors.New("realm directory is incomplete")
	}
	if _, err := uuid.Parse(requesterRealmID); err != nil {
		return nil, errors.New("realm directory requester must be UUID")
	}
	grants := map[string]RealmGrantRecord{}
	if repoID != "" {
		repo, err := p.loadActiveRepository(repoID)
		if err != nil || repo.OwnerRealmID != requesterRealmID {
			return nil, errors.New("authenticated realm does not own repository")
		}
		grantEntries, err := os.ReadDir(filepath.Join(p.ServiceWC, "admin", "grants", repoID))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		for _, entry := range grantEntries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(p.ServiceWC, "admin", "grants", repoID, entry.Name()))
			if err != nil {
				return nil, err
			}
			var grant RealmGrantRecord
			if json.Unmarshal(raw, &grant) != nil || validateRealmGrantRecord(grant) != nil || grant.RepoID != repoID || grant.OwnerRealmID != requesterRealmID {
				return nil, errors.New("canonical realm grant is invalid")
			}
			grants[grant.RecipientRealmID] = grant
		}
	}
	entries, err := os.ReadDir(filepath.Join(p.ServiceWC, "admin", "realms"))
	if err != nil {
		return nil, err
	}
	var result []RealmGrantRecipient
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		record, err := readRealmRecord(filepath.Join(p.ServiceWC, "admin", "realms", entry.Name()))
		if err != nil {
			return nil, err
		}
		grant, hasGrant := grants[record.RealmID]
		if record.RealmID == requesterRealmID || record.State != "active" || record.Alias == "" || (record.DirectoryVisibility != "listed" && (!hasGrant || grant.State != "active")) {
			continue
		}
		result = append(result, RealmGrantRecipient{RealmID: record.RealmID, Alias: record.Alias, Access: grant.Access, State: grant.State})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Alias == result[j].Alias {
			return result[i].RealmID < result[j].RealmID
		}
		return result[i].Alias < result[j].Alias
	})
	return result, nil
}

func (p ServicePublisher) SetRealmDirectoryVisibility(ctx context.Context, realmID, visibility string) (string, error) {
	if !filepath.IsAbs(p.ServiceWC) || p.Runner == nil {
		return "", errors.New("realm directory is incomplete")
	}
	if visibility != "hidden" && visibility != "listed" {
		return "", errors.New("realm directory visibility must be hidden or listed")
	}
	if _, err := uuid.Parse(realmID); err != nil {
		return "", errors.New("realm directory realm_id must be UUID")
	}
	path := filepath.Join(p.ServiceWC, "admin", "realms", realmID+".json")
	record, err := readRealmRecord(path)
	if err != nil || record.RealmID != realmID || record.State != "active" {
		return "", errors.New("authenticated realm record is invalid")
	}
	if visibility == "listed" && record.Alias == "" {
		return "", errors.New("realm must claim an alias before directory listing")
	}
	if record.DirectoryVisibility == visibility {
		return visibility, nil
	}
	record.DirectoryVisibility = visibility
	if err := atomicJSON(path, record); err != nil {
		return "", err
	}
	if err := p.Runner.Publish(ctx, []string{path}, "filees: set realm directory visibility "+visibility); err != nil {
		return "", err
	}
	return visibility, nil
}

func (p ServicePublisher) RebuildGrantAuthority(ctx context.Context) error {
	if err := p.validateGrantPublisher(); err != nil {
		return err
	}
	changed, err := p.rebuildGrantAuthority()
	if err != nil || len(changed) == 0 {
		return err
	}
	hasVersionedChange := false
	wc := filepath.Clean(p.ServiceWC)
	for _, path := range changed {
		clean := filepath.Clean(path)
		if clean == wc || strings.HasPrefix(clean, wc+string(filepath.Separator)) {
			hasVersionedChange = true
			break
		}
	}
	if !hasVersionedChange {
		return nil
	}
	return p.Runner.Publish(ctx, changed, "filees: rebuild realm grant authority")
}

func (p ServicePublisher) rebuildGrantAuthority() ([]string, error) {
	repositories, grants, clients, err := p.loadGrantModel()
	if err != nil {
		return nil, err
	}
	now := p.now()
	changed := []string{}
	effective := map[string]map[string]string{}
	clientIDs := make([]string, 0, len(clients))
	for id := range clients {
		clientIDs = append(clientIDs, id)
	}
	sort.Strings(clientIDs)
	for _, clientID := range clientIDs {
		viewPath := filepath.Join(p.ServiceWC, "clients", clientID, "view.json")
		view, err := clientview.Load(viewPath)
		if err != nil {
			return nil, err
		}
		client := clients[clientID]
		if view.RealmID != client.RealmID {
			return nil, errors.New("client projection realm conflicts with canonical client")
		}
		realm, err := readRealmRecord(filepath.Join(p.ServiceWC, "admin", "realms", view.RealmID+".json"))
		if err != nil || realm.Schema != "filees.realm/v1" || realm.RealmID != view.RealmID || realm.State != "active" {
			return nil, fmt.Errorf("client projection realm record is invalid: realm_id=%s schema=%q state=%q: %v", view.RealmID, realm.Schema, realm.State, err)
		}
		next := projectedRepositories(repositories, grants, view.RealmID, view.ClientRole, client.Kind, view.Repositories)
		effective[clientID] = map[string]string{}
		for _, repository := range next {
			effective[clientID][repository.RepoID] = repository.Access
		}
		if reflect.DeepEqual(view.Repositories, next) && view.RealmAlias == realm.Alias {
			continue
		}
		// Repository grants and realm identity are one canonical projection.
		// Rebuilding either must repair an alias omitted by older activation
		// code, including already-attached clients of an existing realm.
		view.Repositories, view.RealmAlias, view.Generation, view.GeneratedAt = next, realm.Alias, view.Generation+1, now
		if _, err := clientview.StoreIfNewer(viewPath, view); err != nil {
			return nil, err
		}
		changed = append(changed, viewPath)
	}
	authz := renderCanonicalGrantAuthz(repositories, clients, effective)
	current, readErr := os.ReadFile(p.DataAuthzFile)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return nil, readErr
	}
	if !reflect.DeepEqual(current, authz) {
		if err := atomicBytes(p.DataAuthzFile, authz); err != nil {
			return nil, err
		}
		changed = append(changed, p.DataAuthzFile)
	}
	return changed, nil
}

func (p ServicePublisher) validateGrantPublisher() error {
	if !filepath.IsAbs(p.ServiceWC) || !filepath.IsAbs(p.DataAuthzFile) || p.Runner == nil {
		return errors.New("realm grant publisher is incomplete")
	}
	return nil
}
func (p ServicePublisher) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}

func (p ServicePublisher) loadActiveRepository(repoID string) (repositoryRecord, error) {
	path, err := repositoryRecordPath(p.ServiceWC, repoID)
	if err != nil {
		return repositoryRecord{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return repositoryRecord{}, err
	}
	var record repositoryRecord
	if json.Unmarshal(raw, &record) != nil || record.Schema != RepositorySchema || record.RepoID != repoID || record.State != "active" {
		return repositoryRecord{}, errors.New("canonical active repository is invalid")
	}
	return record, nil
}

func (p ServicePublisher) loadGrantModel() (map[string]repositoryRecord, map[string]RealmGrantRecord, map[string]canonicalGrantClient, error) {
	repositories := map[string]repositoryRecord{}
	repoEntries, err := os.ReadDir(filepath.Join(p.ServiceWC, "admin", "repositories"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil, err
	}
	for _, entry := range repoEntries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(p.ServiceWC, "admin", "repositories", entry.Name()))
		if err != nil {
			return nil, nil, nil, err
		}
		var record repositoryRecord
		if json.Unmarshal(raw, &record) != nil || record.Schema != RepositorySchema {
			return nil, nil, nil, errors.New("canonical repository record is invalid")
		}
		if record.State != "deleted" {
			repositories[record.RepoID] = record
		}
	}
	grants := map[string]RealmGrantRecord{}
	grantRoot := filepath.Join(p.ServiceWC, "admin", "grants")
	err = filepath.WalkDir(grantRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if errors.Is(walkErr, os.ErrNotExist) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var grant RealmGrantRecord
		if json.Unmarshal(raw, &grant) != nil || validateRealmGrantRecord(grant) != nil {
			return errors.New("canonical realm grant is invalid")
		}
		repo, ok := repositories[grant.RepoID]
		if grant.State == "active" && (!ok || repo.OwnerRealmID != grant.OwnerRealmID) {
			return errors.New("canonical realm grant owner conflicts with repository")
		}
		if grant.State == "active" {
			grants[grant.RepoID+":"+grant.RecipientRealmID] = grant
		}
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil, err
	}
	clients := map[string]canonicalGrantClient{}
	clientEntries, err := os.ReadDir(filepath.Join(p.ServiceWC, "admin", "clients"))
	if err != nil {
		return nil, nil, nil, err
	}
	for _, entry := range clientEntries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		var record struct {
			Schema   string `json:"schema"`
			ClientID string `json:"client_id"`
			RealmID  string `json:"realm_id"`
			State    string `json:"state"`
			Kind     string `json:"kind,omitempty"`
		}
		if err := decodeJSONFile(filepath.Join(p.ServiceWC, "admin", "clients", entry.Name()), &record); err != nil {
			return nil, nil, nil, err
		}
		if record.Schema != "filees.client-instance/v1" || record.State != "active" {
			continue
		}
		if _, err := uuid.Parse(record.ClientID); err != nil {
			return nil, nil, nil, errors.New("canonical client record is invalid")
		}
		clients[record.ClientID] = canonicalGrantClient{RealmID: record.RealmID, Kind: record.Kind}
	}
	return repositories, grants, clients, nil
}

func (p ServicePublisher) revokeRepositoryGrants(repoID string, now time.Time) ([]string, error) {
	root := filepath.Join(p.ServiceWC, "admin", "grants", repoID)
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	changed := []string{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(root, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var grant RealmGrantRecord
		if json.Unmarshal(raw, &grant) != nil || validateRealmGrantRecord(grant) != nil || grant.RepoID != repoID {
			return nil, errors.New("canonical realm grant is invalid")
		}
		if grant.State == "revoked" {
			continue
		}
		grant.State, grant.Access, grant.PathOwnerPolicy, grant.UpdatedAt = "revoked", "", "", now
		if err := atomicJSON(path, grant); err != nil {
			return nil, err
		}
		changed = append(changed, path)
	}
	return changed, nil
}

func projectedRepositories(repositories map[string]repositoryRecord, grants map[string]RealmGrantRecord, realmID, role, kind string, current []clientview.Repository) []clientview.Repository {
	var result []clientview.Repository
	currentByRepo := map[string]clientview.Repository{}
	for _, repo := range current {
		currentByRepo[repo.RepoID] = repo
	}
	allowedMobile := map[string]clientview.Repository{}
	if kind == "mobile" {
		for _, repo := range current {
			allowedMobile[repo.RepoID] = repo
		}
	}
	for _, repo := range repositories {
		access := ""
		if repo.OwnerRealmID == realmID {
			access = "rw"
		} else if grant, ok := grants[repo.RepoID+":"+realmID]; ok {
			access = grant.Access
		}
		if access == "" {
			continue
		}
		attachmentPolicy, metadataDigest := "optional", ""
		if existing, ok := currentByRepo[repo.RepoID]; ok {
			if existing.AttachmentPolicy != "" {
				attachmentPolicy = existing.AttachmentPolicy
			}
			metadataDigest = existing.MetadataDigest
		}
		if kind == "mobile" {
			requested, ok := allowedMobile[repo.RepoID]
			if !ok {
				continue
			}
			if requested.Access == "r" {
				access = "r"
			}
			attachmentPolicy, metadataDigest = requested.AttachmentPolicy, requested.MetadataDigest
		}
		if role == "ro" {
			access = "r"
		}
		result = append(result, clientview.Repository{RepoID: repo.RepoID, DisplayName: repo.DisplayName, URL: repo.URL, Access: access, State: repo.State, OwnerRealmID: repo.OwnerRealmID, AttachmentPolicy: attachmentPolicy, MetadataDigest: metadataDigest})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].RepoID < result[j].RepoID })
	return result
}

func renderCanonicalGrantAuthz(repositories map[string]repositoryRecord, clients map[string]canonicalGrantClient, effective map[string]map[string]string) []byte {
	repoIDs := make([]string, 0, len(repositories))
	for id := range repositories {
		repoIDs = append(repoIDs, id)
	}
	sort.Strings(repoIDs)
	var out strings.Builder
	out.WriteString("[groups]\n")
	for _, repoID := range repoIDs {
		repo := repositories[repoID]
		owners, writers, readers := []string{}, []string{}, []string{}
		for clientID, repositories := range effective {
			access := repositories[repoID]
			if access == "" {
				continue
			}
			if access == "r" {
				readers = append(readers, clientID)
			} else if clients[clientID].RealmID == repo.OwnerRealmID {
				owners = append(owners, clientID)
			} else {
				writers = append(writers, clientID)
			}
		}
		sort.Strings(owners)
		sort.Strings(writers)
		sort.Strings(readers)
		fmt.Fprintf(&out, "owner-%s = %s\nwriter-%s = %s\nreader-%s = %s\n", repoID, strings.Join(owners, ","), repoID, strings.Join(writers, ","), repoID, strings.Join(readers, ","))
	}
	for _, repoID := range repoIDs {
		fmt.Fprintf(&out, "\n[%s:/]\n@owner-%s = rw\n@writer-%s = rw\n@reader-%s = r\n* =\n", repoID, repoID, repoID, repoID)
	}
	return []byte(out.String())
}

func validateRealmGrantRecord(record RealmGrantRecord) error {
	if record.Schema != RealmGrantSchema || record.CreatedAt.IsZero() || record.UpdatedAt.IsZero() {
		return errors.New("realm grant record is invalid")
	}
	for _, value := range []string{record.RepoID, record.OwnerRealmID, record.RecipientRealmID} {
		if _, err := uuid.Parse(value); err != nil {
			return errors.New("realm grant record contains invalid UUID")
		}
	}
	if record.OwnerRealmID == record.RecipientRealmID {
		return errors.New("realm grant cannot target owner")
	}
	if record.State == "active" {
		if record.Access != "r" && record.Access != "rw" {
			return errors.New("active realm grant access is invalid")
		}
		if (record.Access == "rw") != (record.PathOwnerPolicy == "first_committer") {
			return errors.New("realm grant path owner policy is invalid")
		}
	} else if record.State != "revoked" || record.Access != "" || record.PathOwnerPolicy != "" {
		return errors.New("revoked realm grant is invalid")
	}
	return nil
}
