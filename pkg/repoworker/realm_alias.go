package repoworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filees/pkg/clientview"
	"filees/pkg/realmalias"

	"github.com/google/uuid"
)

var (
	ErrAliasUnavailable = errors.New("realm alias is unavailable")
	ErrAliasImmutable   = errors.New("realm alias is immutable")
)

// RealmAliasStore is intentionally server-side. It has no method to search
// aliases; callers can only claim their authenticated realm's alias or
// resolve already-observed opaque SVN owners.
type RealmAliasStore interface {
	Claim(context.Context, string, string) (string, error)
	Resolve(context.Context, []string) (map[string]string, error)
}

type RealmAliases struct {
	ServiceWC string
	Runner    PublishRunner
}

type realmRecord struct {
	Schema              string    `json:"schema"`
	RealmID             string    `json:"realm_id"`
	State               string    `json:"state"`
	CreatedAt           time.Time `json:"created_at"`
	Alias               string    `json:"alias,omitempty"`
	DirectoryVisibility string    `json:"directory_visibility,omitempty"`
}

type clientRecord struct {
	Schema   string `json:"schema"`
	ClientID string `json:"client_id"`
	RealmID  string `json:"realm_id"`
}

func (store RealmAliases) Claim(ctx context.Context, realmID, alias string) (string, error) {
	if !filepath.IsAbs(store.ServiceWC) || store.Runner == nil {
		return "", errors.New("realm alias store is incomplete")
	}
	if _, err := uuid.Parse(realmID); err != nil {
		return "", errors.New("realm alias realm ID must be UUID")
	}
	canonical, err := realmalias.Normalize(alias)
	if err != nil {
		return "", err
	}
	realmPath := filepath.Join(store.ServiceWC, "admin", "realms", realmID+".json")
	record, err := readRealmRecord(realmPath)
	if err != nil {
		return "", err
	}
	if record.RealmID != realmID || record.Schema != "filees.realm/v1" || record.State != "active" {
		return "", errors.New("authenticated realm record is invalid")
	}
	if record.Alias != "" {
		if record.Alias == canonical {
			return canonical, nil
		}
		return "", ErrAliasImmutable
	}
	entries, err := os.ReadDir(filepath.Dir(realmPath))
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || entry.Name() == filepath.Base(realmPath) {
			continue
		}
		other, err := readRealmRecord(filepath.Join(filepath.Dir(realmPath), entry.Name()))
		if err != nil || other.Alias == "" {
			continue
		}
		if other.Alias == canonical {
			return "", ErrAliasUnavailable
		}
	}
	record.Alias = canonical
	if err := atomicJSON(realmPath, record); err != nil {
		return "", err
	}
	changed := []string{realmPath}
	clientsRoot := filepath.Join(store.ServiceWC, "clients")
	entries, err = os.ReadDir(clientsRoot)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		viewPath := filepath.Join(clientsRoot, entry.Name(), "view.json")
		changedView, err := setViewRealmAlias(viewPath, realmID, canonical)
		if err == nil {
			if changedView {
				changed = append(changed, viewPath)
			}
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	if err := store.Runner.Publish(ctx, changed, "filees: claim realm alias "+canonical); err != nil {
		return "", err
	}
	return canonical, nil
}

func (store RealmAliases) Resolve(_ context.Context, ownerIDs []string) (map[string]string, error) {
	if !filepath.IsAbs(store.ServiceWC) {
		return nil, errors.New("realm alias store is incomplete")
	}
	labels := make(map[string]string, len(ownerIDs))
	for _, ownerID := range ownerIDs {
		if _, err := uuid.Parse(ownerID); err != nil {
			continue
		}
		clientPath := filepath.Join(store.ServiceWC, "admin", "clients", ownerID+".json")
		var client clientRecord
		if err := decodeJSONFile(clientPath, &client); err != nil || client.ClientID != ownerID {
			continue
		}
		if _, err := uuid.Parse(client.RealmID); err != nil {
			continue
		}
		realm, err := readRealmRecord(filepath.Join(store.ServiceWC, "admin", "realms", client.RealmID+".json"))
		if err != nil || realm.RealmID != client.RealmID || realm.Alias == "" {
			continue
		}
		if label, err := realmalias.Normalize(realm.Alias); err == nil {
			labels[ownerID] = label
		}
	}
	return labels, nil
}

func readRealmRecord(path string) (realmRecord, error) {
	var record realmRecord
	if err := decodeJSONFile(path, &record); err != nil {
		return realmRecord{}, err
	}
	if record.Alias != "" {
		canonical, err := realmalias.Normalize(record.Alias)
		if err != nil {
			return realmRecord{}, fmt.Errorf("invalid stored realm alias: %w", err)
		}
		record.Alias = canonical
	}
	if record.DirectoryVisibility != "" && record.DirectoryVisibility != "hidden" && record.DirectoryVisibility != "listed" {
		return realmRecord{}, errors.New("invalid stored realm directory visibility")
	}
	return record, nil
}

func decodeJSONFile(path string, dst any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}

func setViewRealmAlias(path, realmID, alias string) (bool, error) {
	view, err := clientview.Load(path)
	if err != nil {
		return false, err
	}
	if view.RealmID != realmID || view.RealmAlias == alias {
		return false, nil
	}
	view.RealmAlias = alias
	view.Generation++
	view.GeneratedAt = time.Now().UTC()
	changed, err := clientview.StoreIfNewer(path, view)
	return changed, err
}
