package mobileworker

import (
	"context"
	"errors"
	"io"

	v1 "filees/pkg/mobile/v1"
)

// ErrAccessDenied is returned when the authority grants no read access to the
// requested repository for the calling installation.
var ErrAccessDenied = errors.New("mobile: access denied")

// View is the resolved authority for one (installation, repository) pair. It is
// derived server-side from the authenticated session and the grant graph — never
// from client payload.
type View struct {
	RepoPath   string // local repository path
	Generation int64  // control-plane view generation (grants, policy, shouts)
	Access     string // "r" | "rw" | "" (none)
}

func (v View) readable() bool { return v.Access == "r" || v.Access == "rw" }

// RepositoryGrant is one share from the installation's realm projection.
// DisplayName is presentation; RepoID is the only identifier later operations
// may send. Mobile never creates repositories.
type RepositoryGrant struct {
	RepoID      string
	DisplayName string
	Access      string
	State       string
}

// Projection is the authenticated installation's current realm view.
type Projection struct {
	RealmID      string
	RealmAlias   string
	Generation   int64
	Repositories []RepositoryGrant
}

// Authority resolves repository path, control-plane generation and access for the
// authenticated installation. It is the single trusted source of authorization.
type Authority interface {
	Resolve(ctx context.Context, clientID, repoID string) (View, error)
	List(ctx context.Context, clientID string) (Projection, error)
}

// Reader is the read-only repository surface the browser needs.
type Reader interface {
	Youngest(ctx context.Context, repoPath string) (int64, error)
	List(ctx context.Context, repoPath string, rev int64) ([]v1.ManifestEntry, error)
	Cat(ctx context.Context, repoPath, path string, rev int64, w io.Writer) (int64, string, error)
}

// Browser serves the read-only mobile operations. It holds no working copy.
type Browser struct {
	Authority Authority
	Reader    Reader
}

// RefreshManifest returns NOT_MODIFIED only when both the control-plane view
// generation and the repo revision are unchanged; otherwise a full manifest. The
// two-dimension check is what delivers control-plane changes (grants, shouts)
// that move the generation without a commit.
func (b Browser) RefreshManifest(ctx context.Context, clientID string, p v1.RefreshManifestPayload) (v1.RefreshManifestResult, error) {
	view, err := b.Authority.Resolve(ctx, clientID, p.RepoID)
	if err != nil {
		return v1.RefreshManifestResult{}, err
	}
	if !view.readable() {
		return v1.RefreshManifestResult{}, ErrAccessDenied
	}
	rev, err := b.Reader.Youngest(ctx, view.RepoPath)
	if err != nil {
		return v1.RefreshManifestResult{}, err
	}
	if p.KnownViewGeneration == view.Generation && p.KnownRepoRevision == rev {
		return v1.RefreshManifestResult{NotModified: true}, nil
	}
	entries, err := b.Reader.List(ctx, view.RepoPath, rev)
	if err != nil {
		return v1.RefreshManifestResult{}, err
	}
	manifest := &v1.Manifest{
		Schema:         v1.ManifestSchema,
		RepoID:         p.RepoID,
		ViewGeneration: view.Generation,
		RepoRevision:   rev,
		Complete:       true,
		Entries:        entries,
	}
	if err := manifest.Validate(); err != nil {
		return v1.RefreshManifestResult{}, err
	}
	return v1.RefreshManifestResult{Manifest: manifest}, nil
}

// ListRepositories returns the installation's realm projection. The client
// never invents a repo_id: it picks one of these shares and later operations
// send that identifier.
func (b Browser) ListRepositories(ctx context.Context, clientID string) (v1.ListRepositoriesResult, error) {
	proj, err := b.Authority.List(ctx, clientID)
	if err != nil {
		return v1.ListRepositoriesResult{}, err
	}
	repos := make([]v1.RepositorySummary, 0, len(proj.Repositories))
	for _, repo := range proj.Repositories {
		repos = append(repos, v1.RepositorySummary{
			RepoID:      repo.RepoID,
			DisplayName: repo.DisplayName,
			Access:      repo.Access,
			State:       repo.State,
		})
	}
	res := v1.ListRepositoriesResult{
		ViewGeneration: proj.Generation,
		RealmID:        proj.RealmID,
		RealmAlias:     proj.RealmAlias,
		Repositories:   repos,
	}
	if err := res.Validate(); err != nil {
		return v1.ListRepositoriesResult{}, err
	}
	return res, nil
}

// ReadObject streams the file at the requested path into w and returns its size
// and SHA-256. A directory or an absent path yields an error from the reader.
func (b Browser) ReadObject(ctx context.Context, clientID string, p v1.ReadObjectPayload, w io.Writer) (v1.ReadObjectResult, error) {
	view, err := b.Authority.Resolve(ctx, clientID, p.RepoID)
	if err != nil {
		return v1.ReadObjectResult{}, err
	}
	if !view.readable() {
		return v1.ReadObjectResult{}, ErrAccessDenied
	}
	rev, err := b.Reader.Youngest(ctx, view.RepoPath)
	if err != nil {
		return v1.ReadObjectResult{}, err
	}
	size, sha, err := b.Reader.Cat(ctx, view.RepoPath, p.Path, rev, w)
	if err != nil {
		return v1.ReadObjectResult{}, err
	}
	return v1.ReadObjectResult{Path: p.Path, Size: size, Sha256: sha}, nil
}
