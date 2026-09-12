package uploadworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"filees/pkg/avscan"
	"filees/pkg/namingpolicy"
	"filees/public-shares/channel"
	"filees/public-shares/intake"
)

var (
	ErrIncomplete = errors.New("upload reap is incomplete")
	ErrCollision  = errors.New("upload name already exists")
	ErrRejected   = errors.New("upload rejected by antivirus")
	ErrNotFound   = errors.New("quarantine item not found")

	// errUnindexed marks an accept whose arrival record could not be written.
	// It is internal: the file is committed and the acceptance is real, so it
	// must not reach a caller as a failure. Reap counts it as accepted and
	// separately as unindexed.
	errUnindexed = errors.New("upload accepted but not indexed")
)

type Publisher struct {
	SVNMucc string
	SVNLook string
	Run     func(context.Context, string, ...string) ([]byte, error)
}

type Reaper struct {
	Intake    intake.Store
	Channels  *channel.Store
	ReposRoot string
	TrashRoot string
	Scanner   avscan.Scanner
	Publisher Publisher
	Now       func() time.Time
}

type Result struct {
	Accepted int
	Rejected int
	Failed   int
	// Unindexed counts files that were committed but whose arrival record
	// could not be written. The file is safe and the acceptance is real, so
	// it is not a failure; what is missing is the shelf entry a browser reads.
	// Counted apart because calling it either "accepted" alone or "failed"
	// would be a lie in one direction or the other.
	Unindexed int
}

func (r Reaper) Reap(ctx context.Context) (Result, error) {
	if !filepath.IsAbs(r.Intake.Root) || r.Channels == nil || !filepath.IsAbs(r.ReposRoot) || !filepath.IsAbs(r.TrashRoot) || r.Scanner == nil || !filepath.IsAbs(r.Publisher.SVNMucc) || !filepath.IsAbs(r.Publisher.SVNLook) {
		return Result{}, ErrIncomplete
	}
	if err := r.PurgeExpired(ctx, r.now()); err != nil {
		return Result{}, err
	}
	jobs, err := r.Intake.ListReady()
	if err != nil {
		return Result{}, err
	}
	var summary Result
	for _, job := range jobs {
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		if err := r.Intake.Claim(job.UploadID); err != nil {
			summary.Failed++
			continue
		}
		switch err := r.process(ctx, job); {
		case err == nil:
			summary.Accepted++
		case errors.Is(err, avscan.ErrUnavailable):
			return summary, err
		case errors.Is(err, ErrRejected):
			summary.Rejected++
		case errors.Is(err, errUnindexed):
			summary.Accepted++
			summary.Unindexed++
		default:
			summary.Failed++
		}
	}
	return summary, nil
}

func (r Reaper) process(ctx context.Context, job intake.Record) error {
	payload := r.Intake.PayloadPath(job.UploadID)
	if err := verifyPayload(payload, job); err != nil {
		return r.fail(job.UploadID, err)
	}
	record, err := r.Channels.GetUpload(job.ChannelID)
	if err != nil {
		return r.fail(job.UploadID, err)
	}
	if record.Manifest == nil || record.State != channel.StateActive {
		return r.fail(job.UploadID, errors.New("upload channel is not active"))
	}
	verdict, detail, err := r.Scanner.Scan(ctx, payload)
	if err != nil || verdict == avscan.Unavailable {
		return r.fail(job.UploadID, avscan.ErrUnavailable)
	}
	if verdict == avscan.Infected {
		if err := r.reject(ctx, job, record, payload, detail); err != nil {
			return r.fail(job.UploadID, err)
		}
		if err := r.Intake.Remove(job.UploadID); err != nil {
			return err
		}
		return ErrRejected
	}
	name, err := namingpolicy.TargetName(job.OriginalName)
	if err != nil {
		return r.fail(job.UploadID, err)
	}
	repo := filepath.Join(r.ReposRoot, record.Manifest.UploadRepoID)
	exists, err := r.exists(ctx, repo, name)
	if err != nil {
		return r.fail(job.UploadID, err)
	}
	if exists {
		return r.fail(job.UploadID, ErrCollision)
	}
	revision, err := r.putFile(ctx, repo, payload, name, "filees: accept upload "+job.UploadID, map[string]string{
		"filees:upload-id": job.UploadID, "filees:upload-sha256": job.SHA256, "filees:upload-channel": job.ChannelID,
	})
	if err != nil {
		return r.fail(job.UploadID, err)
	}
	// The commit is the point of no return: the file is in the delivery
	// repository, so the job leaves intake whatever happens next. Retrying
	// would only meet the collision check and fail every minute from now on.
	// A missing arrival record costs the browser an entry, never the file.
	recordErr := r.Channels.RecordAccepted(channel.Accepted{
		ChannelID: job.ChannelID, UploadID: job.UploadID,
		RepoPath: name, OriginalName: job.OriginalName,
		Size: job.Size, SHA256: job.SHA256,
		Revision: revision, AcceptedAt: r.now(),
	})
	if err := r.Intake.Remove(job.UploadID); err != nil {
		return err
	}
	if recordErr != nil {
		return errUnindexed
	}
	return nil
}

func (r Reaper) fail(uploadID string, err error) error {
	_ = r.Intake.Release(uploadID)
	return err
}

func (r Reaper) reject(ctx context.Context, job intake.Record, record channel.UploadRecord, payload, detail string) error {
	day := r.now().UTC().Format("2006-01-02")
	rel := record.Slug + "-" + job.ChannelID + "/" + day + "/" + job.UploadID
	waiting := filepath.Join(r.TrashRoot, filepath.FromSlash(rel))
	if err := os.MkdirAll(waiting, 0700); err != nil {
		return err
	}
	if err := copyFile(payload, filepath.Join(waiting, "payload")); err != nil {
		return err
	}
	indexPath := filepath.Join(waiting, indexName)
	if err := writeIndex(indexPath, Index{
		UploadID: job.UploadID, OwnerRealm: record.OwnerRealm, OriginalName: job.OriginalName, Size: job.Size,
		SHA256: job.SHA256, AVVerdict: detail, RecipientToken: job.TokenSHA256,
		ReceivedAt: job.ReceivedAt.UTC(),
	}); err != nil {
		return err
	}
	trashRepo := filepath.Join(r.ReposRoot, channel.TrashRepositoryID(record.OwnerRealm))
	return r.putTree(ctx, trashRepo, indexPath, rel+"/index.json", "filees: reject upload "+job.UploadID)
}

func (r Reaper) exists(ctx context.Context, repository, name string) (bool, error) {
	raw, err := r.Publisher.run(ctx, r.Publisher.SVNLook, "tree", "--full-paths", repository)
	if err != nil {
		return false, err
	}
	needle := strings.TrimSuffix(name, "/")
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == needle || line == needle+"/" {
			return true, nil
		}
	}
	return false, nil
}

func (r Reaper) putFile(ctx context.Context, repository, payload, repoPath, message string, revprops map[string]string) (int64, error) {
	args := []string{"--non-interactive", "-m", message}
	for key, value := range revprops {
		args = append(args, "--with-revprop", key+"="+value)
	}
	args = append(args, "put", payload, appendURL(fileURL(repository), repoPath))
	out, err := r.Publisher.run(ctx, r.Publisher.SVNMucc, args...)
	if err != nil {
		return 0, err
	}
	return committedRevision(out), nil
}

// committedRevision reads the revision out of svnmucc's own confirmation line,
// which reads "rNNN committed by ...". An unreadable line yields zero rather
// than an error: the commit already succeeded, and the number is a convenience
// for the browser, not part of the guarantee.
func committedRevision(out []byte) int64 {
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "r") {
			continue
		}
		digits := line[1:]
		if cut := strings.IndexByte(digits, ' '); cut >= 0 {
			digits = digits[:cut]
		}
		if revision, err := strconv.ParseInt(digits, 10, 64); err == nil && revision > 0 {
			return revision
		}
	}
	return 0
}

func (r Reaper) putTree(ctx context.Context, repository, payload, repoPath, message string) error {
	args := []string{"--non-interactive", "-m", message}
	parts := strings.Split(repoPath, "/")
	prefix := ""
	for i := 0; i < len(parts)-1; i++ {
		if prefix == "" {
			prefix = parts[i]
		} else {
			prefix += "/" + parts[i]
		}
		exists, err := r.exists(ctx, repository, prefix)
		if err != nil {
			return err
		}
		if !exists {
			args = append(args, "mkdir", appendURL(fileURL(repository), prefix))
		}
	}
	args = append(args, "put", payload, appendURL(fileURL(repository), repoPath))
	_, err := r.Publisher.run(ctx, r.Publisher.SVNMucc, args...)
	return err
}

func (r Reaper) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (p Publisher) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if p.Run != nil {
		return p.Run(ctx, name, args...)
	}
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func verifyPayload(path string, job intake.Record) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, file)
	if err != nil {
		return err
	}
	if n != job.Size || hex.EncodeToString(hash.Sum(nil)) != job.SHA256 {
		return errors.New("upload payload digest mismatch")
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func fileURL(path string) string {
	slashPath := filepath.ToSlash(path)
	if len(slashPath) >= 2 && slashPath[1] == ':' {
		slashPath = "/" + slashPath
	}
	return (&url.URL{Scheme: "file", Path: slashPath}).String()
}

func appendURL(root, relative string) string {
	parts := strings.Split(relative, "/")
	for index := range parts {
		parts[index] = url.PathEscape(parts[index])
	}
	return strings.TrimRight(root, "/") + "/" + strings.Join(parts, "/")
}
