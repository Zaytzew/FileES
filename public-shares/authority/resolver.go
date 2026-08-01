// Package authority resolves Public Shares only on the FileES side of the
// trust boundary. It is the sole package that combines public IDs with
// repository paths and source bytes.
package authority

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"filees/public-shares/channel"
	"filees/public-shares/manifest"
)

var ErrNotFound = errors.New("public share resource not found")

var errLeafTooLarge = errors.New("public share leaf exceeds authority size limit")

type Source interface {
	Head(context.Context, string) (int64, error)
	Cat(context.Context, string, string, int64, io.Writer) error
}

type Resolver struct {
	Channels    *channel.Store
	Source      Source
	FrostKey    []byte
	StagingRoot string
	MaxLeafSize int64
}

type Entry struct {
	Projection channel.Projection `json:"projection"`
	Revision   int64              `json:"revision"`
	FrostProof string             `json:"frost_proof"`
}

type ObjectRequest struct {
	ChannelID  string `json:"channel_id"`
	PublicID   string `json:"public_id"`
	Revision   int64  `json:"revision"`
	FrostProof string `json:"frost_proof"`
}

// ObjectPermit is safe to return to the public service. CacheKey is an opaque
// digest of (repo_id, path, revision); no source identifier or path is exposed.
type ObjectPermit struct {
	CacheKey    string `json:"cache_key"`
	DisplayName string `json:"display_name"`
	Revision    int64  `json:"revision"`
}

type FetchedLeaf struct {
	ObjectPermit
	Size int64
	MD5  string
	Body io.ReadCloser
}

// Inspect returns current public policy without resolving HEAD. It is used on
// every file request so an older visit frost remains usable across ordinary
// repository commits while ACL updates and revocation still take effect.
func (r Resolver) Inspect(alias, channelSlug string) (channel.Projection, error) {
	if err := r.validate(); err != nil {
		return channel.Projection{}, err
	}
	record, err := r.Channels.ResolveAddress(alias, channelSlug)
	if err != nil || r.revalidate(record) != nil {
		return channel.Projection{}, ErrNotFound
	}
	projection, err := r.Channels.Projection(record.ChannelID)
	if err != nil {
		return channel.Projection{}, ErrNotFound
	}
	return projection, nil
}

func (r Resolver) Enter(ctx context.Context, alias, channelSlug string) (Entry, error) {
	if err := r.validate(); err != nil {
		return Entry{}, err
	}
	record, err := r.Channels.ResolveAddress(alias, channelSlug)
	if err != nil || r.revalidate(record) != nil {
		return Entry{}, ErrNotFound
	}
	projection, err := r.Channels.Projection(record.ChannelID)
	if err != nil {
		return Entry{}, ErrNotFound
	}
	head, err := r.Source.Head(ctx, record.Manifest.RepoID)
	if err != nil || head < 1 {
		return Entry{}, ErrNotFound
	}
	revision := head
	if record.Manifest.DoNotFollow != nil {
		revision = *record.Manifest.DoNotFollow
		if revision > head {
			return Entry{}, ErrNotFound
		}
	}
	return Entry{Projection: projection, Revision: revision, FrostProof: r.proof(record, revision)}, nil
}

func (r Resolver) Check(_ context.Context, request ObjectRequest) (ObjectPermit, error) {
	if err := r.validate(); err != nil {
		return ObjectPermit{}, err
	}
	_, object, permit, err := r.resolveObject(request)
	if err != nil {
		return ObjectPermit{}, ErrNotFound
	}
	permit.DisplayName = object.DisplayName
	return permit, nil
}

// Fetch revalidates everything Check validates, then materializes exactly the
// canonical mapped leaf into private staging. MD5 is local corruption
// detection for the subsequent cache push, not a trust proof.
func (r Resolver) Fetch(ctx context.Context, request ObjectRequest) (FetchedLeaf, error) {
	if err := r.validate(); err != nil {
		return FetchedLeaf{}, err
	}
	record, object, permit, err := r.resolveObject(request)
	if err != nil {
		return FetchedLeaf{}, ErrNotFound
	}
	if !filepath.IsAbs(r.StagingRoot) {
		return FetchedLeaf{}, errors.New("public share authority staging root must be absolute")
	}
	if err := os.MkdirAll(r.StagingRoot, 0700); err != nil {
		return FetchedLeaf{}, err
	}
	file, err := os.CreateTemp(r.StagingRoot, ".public-share-leaf-*.tmp")
	if err != nil {
		return FetchedLeaf{}, err
	}
	path := file.Name()
	cleanup := func() { file.Close(); os.Remove(path) }
	if err := file.Chmod(0600); err != nil {
		cleanup()
		return FetchedLeaf{}, err
	}
	hash := md5.New()
	bounded := &boundedLeafWriter{Writer: io.MultiWriter(file, hash), Remaining: r.MaxLeafSize}
	if err := r.Source.Cat(ctx, record.Manifest.RepoID, object.RepoPath, request.Revision, bounded); err != nil {
		cleanup()
		return FetchedLeaf{}, ErrNotFound
	}
	if err := file.Sync(); err != nil {
		cleanup()
		return FetchedLeaf{}, err
	}
	info, err := file.Stat()
	if err != nil {
		cleanup()
		return FetchedLeaf{}, err
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return FetchedLeaf{}, err
	}
	body, err := os.Open(path)
	if err != nil {
		os.Remove(path)
		return FetchedLeaf{}, err
	}
	permit.DisplayName = object.DisplayName
	return FetchedLeaf{ObjectPermit: permit, Size: info.Size(), MD5: hex.EncodeToString(hash.Sum(nil)), Body: &removeOnClose{File: body, path: path}}, nil
}

func (r Resolver) resolveObject(request ObjectRequest) (channel.Record, manifest.Object, ObjectPermit, error) {
	if request.Revision < 1 || request.PublicID == "" || request.FrostProof == "" {
		return channel.Record{}, manifest.Object{}, ObjectPermit{}, ErrNotFound
	}
	record, err := r.Channels.Load(request.ChannelID)
	if err != nil || record.State != channel.StateActive || record.Manifest == nil || r.revalidate(record) != nil {
		return channel.Record{}, manifest.Object{}, ObjectPermit{}, ErrNotFound
	}
	if !hmac.Equal([]byte(r.proof(record, request.Revision)), []byte(request.FrostProof)) {
		return channel.Record{}, manifest.Object{}, ObjectPermit{}, ErrNotFound
	}
	if record.Manifest.DoNotFollow != nil && *record.Manifest.DoNotFollow != request.Revision {
		return channel.Record{}, manifest.Object{}, ObjectPermit{}, ErrNotFound
	}
	for _, object := range record.Manifest.Objects {
		if object.PublicID != request.PublicID {
			continue
		}
		digest := sha256.Sum256([]byte(record.Manifest.RepoID + "\x00" + object.RepoPath + "\x00" + strconv.FormatInt(request.Revision, 10)))
		return record, object, ObjectPermit{CacheKey: hex.EncodeToString(digest[:]), Revision: request.Revision}, nil
	}
	return channel.Record{}, manifest.Object{}, ObjectPermit{}, ErrNotFound
}

func (r Resolver) revalidate(record channel.Record) error {
	if record.Manifest == nil || r.Channels.Authority.OwnsActiveRepository(record.Manifest.OwnerRealm, record.Manifest.RepoID) != nil {
		return ErrNotFound
	}
	alias, err := r.Channels.Authority.ActiveRealmAlias(record.Manifest.OwnerRealm)
	if err != nil || alias != record.Alias {
		return ErrNotFound
	}
	return nil
}

func (r Resolver) proof(record channel.Record, revision int64) string {
	manifestJSON, _ := json.Marshal(record.Manifest)
	recipientsJSON, _ := json.Marshal(record.Recipients)
	fingerprint := sha256.Sum256(append(manifestJSON, recipientsJSON...))
	mac := hmac.New(sha256.New, r.FrostKey)
	fmt.Fprintf(mac, "%s\x00%d\x00%s", record.ChannelID, revision, hex.EncodeToString(fingerprint[:]))
	return hex.EncodeToString(mac.Sum(nil))
}

func (r Resolver) validate() error {
	if r.Channels == nil || r.Channels.Authority == nil || r.Source == nil || len(r.FrostKey) < 32 || r.MaxLeafSize <= 0 {
		return errors.New("public share authority resolver is incomplete")
	}
	return nil
}

type boundedLeafWriter struct {
	io.Writer
	Remaining int64
}

func (w *boundedLeafWriter) Write(p []byte) (int, error) {
	if w.Remaining <= 0 {
		return 0, errLeafTooLarge
	}
	if int64(len(p)) > w.Remaining {
		written, err := w.Writer.Write(p[:w.Remaining])
		w.Remaining -= int64(written)
		if err != nil {
			return written, err
		}
		return written, errLeafTooLarge
	}
	written, err := w.Writer.Write(p)
	w.Remaining -= int64(written)
	return written, err
}

type removeOnClose struct {
	*os.File
	path string
}

func (f *removeOnClose) Close() error {
	err := f.File.Close()
	removeErr := os.Remove(f.path)
	if err != nil {
		return err
	}
	if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return removeErr
	}
	return nil
}
