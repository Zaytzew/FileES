package uploadworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"filees/pkg/avscan"
	"filees/pkg/namingpolicy"
	"filees/pkg/repoworker"
	"filees/public-shares/channel"
	"filees/public-shares/intake"
)

var (
	ErrIncomplete = errors.New("upload reap is incomplete")
	ErrCollision  = errors.New("upload name already exists")
	ErrRejected   = errors.New("upload rejected by antivirus")
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
}

func (r Reaper) Reap(ctx context.Context) (Result, error) {
	if !filepath.IsAbs(r.Intake.Root) || r.Channels == nil || !filepath.IsAbs(r.ReposRoot) || !filepath.IsAbs(r.TrashRoot) || r.Scanner == nil || !filepath.IsAbs(r.Publisher.SVNMucc) || !filepath.IsAbs(r.Publisher.SVNLook) {
		return Result{}, ErrIncomplete
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
		default:
			summary.Failed++
		}
	}
	return summary, nil
}

func (r Reaper) process(ctx context.Context, job intake.Record) error {
	payload := r.Intake.PayloadPath(job.UploadID)
	if err := verifyPayload(payload, job); err != nil {
		return err
	}
	record, err := r.Channels.GetUpload(job.ChannelID)
	if err != nil || record.Manifest == nil || record.State != channel.StateActive {
		return err
	}
	verdict, detail, err := r.Scanner.Scan(ctx, payload)
	if err != nil || verdict == avscan.Unavailable {
		_ = r.Intake.Release(job.UploadID)
		return avscan.ErrUnavailable
	}
	if verdict == avscan.Infected {
		if err := r.reject(ctx, job, record, payload, detail); err != nil {
			return err
		}
		if err := r.Intake.Remove(job.UploadID); err != nil {
			return err
		}
		return ErrRejected
	}
	name, err := namingpolicy.TargetName(job.OriginalName)
	if err != nil {
		return err
	}
	repo := filepath.Join(r.ReposRoot, record.Manifest.UploadRepoID)
	exists, err := r.exists(ctx, repo, name)
	if err != nil {
		return err
	}
	if exists {
		return ErrCollision
	}
	if err := r.putFile(ctx, repo, payload, name, "filees: accept upload "+job.UploadID, map[string]string{
		"filees:upload-id": job.UploadID, "filees:upload-sha256": job.SHA256, "filees:upload-channel": job.ChannelID,
	}); err != nil {
		return err
	}
	return r.Intake.Remove(job.UploadID)
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
	index := map[string]any{
		"upload_id": job.UploadID, "original_name": job.OriginalName, "size": job.Size,
		"sha256": job.SHA256, "av_verdict": detail, "recipient_token": job.TokenSHA256,
		"received_at": job.ReceivedAt.UTC().Format(time.RFC3339Nano),
	}
	raw, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	indexPath := filepath.Join(waiting, "index.json")
	if err := os.WriteFile(indexPath, append(raw, '\n'), 0600); err != nil {
		return err
	}
	trashRepo := filepath.Join(r.ReposRoot, repoworker.UploadTrashRepositoryID(record.OwnerRealm))
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

func (r Reaper) putFile(ctx context.Context, repository, payload, repoPath, message string, revprops map[string]string) error {
	args := []string{"--non-interactive", "-m", message}
	for key, value := range revprops {
		args = append(args, "--with-revprop", key+"="+value)
	}
	args = append(args, "put", payload, appendURL(fileURL(repository), repoPath))
	_, err := r.Publisher.run(ctx, r.Publisher.SVNMucc, args...)
	return err
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
