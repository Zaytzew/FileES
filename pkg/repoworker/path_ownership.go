package repoworker

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"filees/pkg/pathownership"
	"github.com/google/uuid"
)

// SVNPathOwners derives facts from committed SVN history and canonical
// identity/grant records. It does not mutate authority; broker caches may
// discard its output and reconstruct it. Callers authorize repo access first
// and hold the service-WC authority lock across this read and any mutation.
type SVNPathOwners struct{ SVN, RepositoriesRoot, ServiceWC string }

type RepositoryRevision struct {
	Number int64
	UUID   string
}

func (s SVNPathOwners) repositoryURL(repoID string) (string, error) {
	if _, err := uuid.Parse(repoID); err != nil {
		return "", err
	}
	if !filepath.IsAbs(s.SVN) || !filepath.IsAbs(s.RepositoriesRoot) || !filepath.IsAbs(s.ServiceWC) {
		return "", errors.New("ownership authority paths must be absolute")
	}
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(filepath.Join(s.RepositoriesRoot, repoID))}).String(), nil
}
func (s SVNPathOwners) run(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, s.SVN, args...)
	command.Env = append(os.Environ(), "LC_ALL=C")
	var out ownershipOutput
	command.Stdout = &out
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("read SVN ownership history: %w", err)
	}
	return out.Bytes(), nil
}

// Do not embed bytes.Buffer: its promoted ReadFrom would bypass Write's
// limit when os/exec copies stdout using io.Copy.
type ownershipOutput struct{ buffer bytes.Buffer }

func (b *ownershipOutput) Bytes() []byte { return b.buffer.Bytes() }

func (b *ownershipOutput) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > 64<<20 {
		return 0, errors.New("ownership history exceeds 64 MiB")
	}
	return b.buffer.Write(p)
}
func (s SVNPathOwners) Head(ctx context.Context, repoID string) (RepositoryRevision, error) {
	target, err := s.repositoryURL(repoID)
	if err != nil {
		return RepositoryRevision{}, err
	}
	out, err := s.run(ctx, "info", "--xml", "-r", "HEAD", target+"@")
	if err != nil {
		return RepositoryRevision{}, err
	}
	var info struct {
		Entries []struct {
			Revision int64  `xml:"revision,attr"`
			UUID     string `xml:"repository>uuid"`
		} `xml:"entry"`
	}
	if err := xml.Unmarshal(out, &info); err != nil || len(info.Entries) != 1 || info.Entries[0].Revision < 0 {
		return RepositoryRevision{}, errors.New("invalid repository revision")
	}
	if _, err := uuid.Parse(info.Entries[0].UUID); err != nil {
		return RepositoryRevision{}, errors.New("invalid repository incarnation")
	}
	return RepositoryRevision{Number: info.Entries[0].Revision, UUID: info.Entries[0].UUID}, nil
}
func (s SVNPathOwners) Snapshot(ctx context.Context, repoID string) (pathownership.Snapshot, error) {
	target, err := s.repositoryURL(repoID)
	if err != nil {
		return pathownership.Snapshot{}, err
	}
	repo, err := (ServicePublisher{ServiceWC: s.ServiceWC}).loadActiveRepository(repoID)
	if err != nil {
		return pathownership.Snapshot{}, err
	}
	head, err := s.Head(ctx, repoID)
	if err != nil {
		return pathownership.Snapshot{}, err
	}
	var revisions []pathownership.Revision
	if head.Number > 0 {
		raw, err := s.run(ctx, "log", "--xml", "--verbose", "--quiet", "-r", "1:"+strconv.FormatInt(head.Number, 10), target+"@"+strconv.FormatInt(head.Number, 10))
		if err != nil {
			return pathownership.Snapshot{}, err
		}
		revisions, err = pathownership.ParseLog(raw)
		if err != nil {
			return pathownership.Snapshot{}, err
		}
	}
	snapshot, err := pathownership.Replay(ctx, repoID+":"+head.UUID, head.Number, revisions)
	if err != nil {
		return pathownership.Snapshot{}, err
	}
	owners := map[string]string{}
	if _, err := uuid.Parse(repo.OwnerRealmID); err != nil {
		return pathownership.Snapshot{}, ErrPathOwnerUnavailable
	}
	grants := map[string]*RealmGrantRecord{}
	ownerRealm, err := readRealmRecord(filepath.Join(s.ServiceWC, "admin", "realms", repo.OwnerRealmID+".json"))
	if err != nil || ownerRealm.Schema != "filees.realm/v1" || ownerRealm.RealmID != repo.OwnerRealmID || ownerRealm.State != "active" {
		return pathownership.Snapshot{}, ErrPathOwnerUnavailable
	}
	for i := range snapshot.Entries {
		entry := &snapshot.Entries[i]
		realm, known := owners[entry.FirstCommitter]
		if !known {
			if _, err := uuid.Parse(entry.FirstCommitter); err != nil {
				// Historical imports and server maintenance have no activation identity.
				// Their operational backstop is the importing repository's owner.
				realm = repo.OwnerRealmID
			} else {
				identity, err := (PassportReplacementAuthority{ServiceWC: s.ServiceWC}).client(entry.FirstCommitter)
				if err != nil {
					return pathownership.Snapshot{}, ErrPathOwnerUnavailable
				}
				realm = identity.RealmID
			}
			owners[entry.FirstCommitter] = realm
		}
		entry.OwnerRealmID = repo.OwnerRealmID
		if realm == repo.OwnerRealmID {
			continue
		}
		grant, loaded := grants[realm]
		if !loaded {
			realmRecord, err := readRealmRecord(filepath.Join(s.ServiceWC, "admin", "realms", realm+".json"))
			if err != nil {
				return pathownership.Snapshot{}, err
			}
			if realmRecord.Schema != "filees.realm/v1" || realmRecord.RealmID != realm {
				return pathownership.Snapshot{}, ErrPathOwnerUnavailable
			}
			if realmRecord.State == "active" {
				p, err := realmGrantPath(s.ServiceWC, repoID, realm)
				if err != nil {
					return pathownership.Snapshot{}, err
				}
				var g RealmGrantRecord
				err = decodeJSONFile(p, &g)
				if err != nil && !errors.Is(err, os.ErrNotExist) {
					return pathownership.Snapshot{}, err
				}
				if err == nil {
					if validateRealmGrantRecord(g) != nil || g.RepoID != repoID || g.OwnerRealmID != repo.OwnerRealmID || g.RecipientRealmID != realm {
						return pathownership.Snapshot{}, ErrPathOwnerUnavailable
					}
					grant = &g
				}
			}
			grants[realm] = grant
		}
		if grant != nil && grant.State == "active" && grant.Access == "rw" {
			// An old record cannot prove that no earlier revoke/regrant took
			// place. Do not resurrect rights or silently assign them elsewhere.
			if grant.PathOwnerCutoffRevision == nil || grant.PathOwnerRepositoryUUID == "" {
				return pathownership.Snapshot{}, ErrPathOwnerUnavailable
			}
			if grant.PathOwnerRepositoryUUID == head.UUID {
				if *grant.PathOwnerCutoffRevision > head.Number {
					return pathownership.Snapshot{}, ErrPathOwnerUnavailable
				}
				if entry.CreatedRevision > *grant.PathOwnerCutoffRevision {
					entry.OwnerRealmID = realm
				}
			}
		}
	}
	finalHead, err := s.Head(ctx, repoID)
	if err != nil || finalHead != head {
		return pathownership.Snapshot{}, ErrPathOwnerUnavailable
	}
	return snapshot, nil
}
func (s SVNPathOwners) PathOwner(ctx context.Context, repoID, relativePath string) (string, error) {
	if err := validateLockReleasePath(relativePath); err != nil {
		return "", err
	}
	snapshot, err := s.Snapshot(ctx, repoID)
	if err != nil {
		return "", err
	}
	for _, entry := range snapshot.Entries {
		if entry.Path == relativePath {
			return entry.OwnerRealmID, nil
		}
	}
	return "", ErrPathOwnerUnavailable
}
