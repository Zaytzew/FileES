package servertool

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"filees/internal/obsandbox"
	"filees/pkg/clientview"
	reservationv1 "filees/pkg/reservation/v1"
	"filees/pkg/reservationprojection"
	"filees/pkg/serverconfig"
)

// errReservationAccessDenied mirrors whaleworker.ErrAccessDenied: an
// authorization failure is a distinct, out-of-band process failure, never
// encoded as a reservationv1.Result. A client asking about a repository
// outside its own active view gets no answer at all, not an "unknown".
var errReservationAccessDenied = errors.New("filees-serving-state: repository is not in the client's active view")

// reservationWorkerMaxStderr bounds how much of svn's stderr this worker
// will hold in memory when reporting a failed live refresh.
const reservationWorkerMaxStderr = 4 << 10

// reservationWorkerMaxRequest bounds the reservationv1.Request read from
// stdin. A Request is just a schema string and one UUID, so this is
// generous headroom, not a tight fit — anything near it is already
// malformed input, not a legitimate oversized request.
const reservationWorkerMaxRequest = 4 << 10

// reservationWorkerPromises: this worker only reads the target repository
// (svn info never mutates it) and writes its own artifact directory —
// unlike whaleWorkerPromises/svnExecPromises it needs no wpath/cpath/fattr
// on the repository root itself, only on its own state directory. It keeps
// "exec" in its own runtime promises (unlike a worker that never spawns a
// further child) because queryLiveLocks execs svn itself.
// reservationWorkerBootstrapPromises/reservationWorkerRuntimePromises
// deliberately mirror workerPromises/svnPromises (common.go): prot_exec is
// never part of this process's own active promises, only of svnExecPromises
// — the ceiling handed to svn itself when it is exec'd. filees-worker's own
// proven-working repository-control path follows exactly this shape.
const (
	reservationWorkerBootstrapPromises = "stdio rpath wpath cpath fattr flock proc exec"
	reservationWorkerRuntimePromises   = "stdio rpath wpath cpath fattr flock proc exec"
)

// RunReservationProjectionWorker is cmd/filees-serving-state's entrypoint.
// It answers exactly one request read from stdin and writes exactly one
// reservationv1.Result to stdout, matching the one-shot, one-ticket shape
// of the existing control-plane workers (pkg/controlclient /
// internal/servertool/client_entry.go's ClientControlCommand), but over
// its own forced SSH command and its own binary — see
// concepts/RESERVATION_SERVER_EMISSION_WORKPLAN.md §"Granica procesu".
func RunReservationProjectionWorker(args []string, in io.Reader, out, stderr io.Writer) int {
	path, rest, err := configPath(args)
	if err != nil {
		report(stderr, "serving-state arguments", err)
		return ExitUsage
	}
	return runReservationProjectionWorker(path, rest, in, out, stderr)
}

func runReservationProjectionWorker(configPath string, args []string, in io.Reader, out, stderr io.Writer) int {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		fmt.Fprintln(stderr, "filees-serving-state: client ID required")
		return ExitUsage
	}
	clientID := args[0]
	if err := sandboxBegin(reservationWorkerBootstrapPromises); err != nil {
		report(stderr, "serving-state bootstrap sandbox", err)
		return ExitSoftware
	}
	config, err := serverconfig.LoadFor(configPath, serverconfig.SecretActivation)
	if err != nil {
		report(stderr, "serving-state config", err)
		return ExitConfig
	}
	r := config.Repositories
	if !filepath.IsAbs(r.Root) || !filepath.IsAbs(r.ResultsRoot) || !filepath.IsAbs(config.Activation.SVNBinary) || !filepath.IsAbs(config.Activation.ServiceWorkingCopy) {
		fmt.Fprintln(stderr, "serving-state: repository configuration is incomplete")
		return ExitConfig
	}
	stateRoot := filepath.Join(r.ResultsRoot, "reservation-projection")
	if err := os.MkdirAll(stateRoot, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		report(stderr, "serving-state artifact directory", err)
		return ExitConfig
	}
	if err := sandboxApplyForExec(reservationWorkerProfile(), svnExecPromises); err != nil {
		report(stderr, "serving-state sandbox", err)
		return ExitSoftware
	}

	raw, err := io.ReadAll(io.LimitReader(in, reservationWorkerMaxRequest+1))
	if err != nil {
		report(stderr, "serving-state request read", err)
		return ExitData
	}
	if len(raw) > reservationWorkerMaxRequest {
		fmt.Fprintln(stderr, "filees-serving-state: request exceeds size limit")
		return ExitData
	}
	req, err := reservationv1.ParseRequest(bytes.TrimSpace(raw))
	if err != nil {
		report(stderr, "serving-state request parse", err)
		return ExitData
	}
	view, err := authorizedClientView(config.Activation.ServiceWorkingCopy, clientID, req.RepoID)
	if err != nil {
		report(stderr, "serving-state authorization", err)
		return ExitSoftware
	}

	store := reservationprojection.NewStore(stateRoot)
	result := refreshReservationProjection(context.Background(), store, config.Activation.SVNBinary, r.Root, req.RepoID)
	// The view was loaded to authorize this request, so saying when the server
	// last produced it costs nothing and tells the client the one thing it
	// cannot work out locally.
	result.ViewGeneration = view.Generation
	if !view.GeneratedAt.IsZero() {
		produced := view.GeneratedAt
		result.ViewGeneratedAt = &produced
	}
	payload, err := json.Marshal(result)
	if err != nil {
		report(stderr, "serving-state result encode", err)
		return ExitSoftware
	}
	if _, err := out.Write(append(payload, '\n')); err != nil {
		report(stderr, "serving-state result write", err)
		return ExitSoftware
	}
	return ExitOK
}

// authorizeReservationRequest mirrors clientviewWhaleAuthority.ResolveWhale:
// a client may only ask about a repository present, and active, in its own
// server-issued view. clientview is server-authoritative (rewritten by the
// activation/repository workers, never by this one), so this is a read-only
// check against already-decided access, not a second access-control model.
func authorizeReservationRequest(serviceWC, clientID, repoID string) error {
	_, err := authorizedClientView(serviceWC, clientID, repoID)
	return err
}

// authorizedClientView performs the same check and hands back the view it had
// to read, so the caller can report when the server last produced it without a
// second read of the same file.
func authorizedClientView(serviceWC, clientID, repoID string) (clientview.View, error) {
	view, err := clientview.Load(filepath.Join(serviceWC, "clients", clientID, "view.json"))
	if err != nil || view.ClientID != clientID {
		return clientview.View{}, errReservationAccessDenied
	}
	for _, repository := range view.Repositories {
		if repository.RepoID == repoID && repository.State == "active" {
			return view, nil
		}
	}
	return clientview.View{}, errReservationAccessDenied
}

// reservationWorkerProfile deliberately unveils nothing of its own.
// filees-client-entry (internal/servertool/client_entry.go's
// ClientReservationCommand branch) already unveiled everything this worker
// needs — server config, service working copy, repository root, the svn
// binary, its own artifact directory — before exec'ing this binary.
// OpenBSD's unveil(2) restrictions are inherited across exec and, once the
// parent has locked its table (unveil(NULL,NULL), done by
// obsandbox.ApplyForExec), a child process can no longer call unveil()
// itself at all — not even to redeclare an already-visible path with
// identical permissions. That call fails with EPERM ("unveil ...: operation
// not permitted"), which is exactly the live failure this profile fixes:
// the first version of this function tried to re-unveil paths the parent
// had already granted. This process only needs to narrow its own pledge
// promises (see runReservationProjectionWorker's ApplyForExec call), never
// to see anything new.
func reservationWorkerProfile() obsandbox.Profile {
	return obsandbox.Profile{Name: "filees-serving-state", Promises: reservationWorkerRuntimePromises}
}

// refreshReservationProjection is the pure decision logic (fresh vs stale vs
// unknown), factored out from process/sandbox setup so it can be unit
// tested with a fake queryLocks — see reservation_projection_worker_test.go.
func refreshReservationProjection(ctx context.Context, store *reservationprojection.Store, svnBinary, reposRoot, repoID string) reservationv1.Result {
	return refreshReservationProjectionWith(store, repoID, func() ([]reservationv1.Reservation, error) {
		return queryLiveLocks(ctx, svnBinary, filepath.Join(reposRoot, repoID))
	})
}

func refreshReservationProjectionWith(store *reservationprojection.Store, repoID string, queryLocks func() ([]reservationv1.Reservation, error)) reservationv1.Result {
	art, refreshErr := store.Refresh(repoID, func(reservationprojection.Artifact, bool) ([]reservationv1.Reservation, error) {
		return queryLocks()
	})
	if refreshErr == nil {
		return reservationv1.Result{
			Schema:       reservationv1.Schema,
			RepoID:       repoID,
			Reservations: art.Reservations,
			AsOf:         art.AsOf,
			Generation:   strconv.FormatInt(art.Generation, 10),
		}
	}
	prev, ok, loadErr := store.Load(repoID)
	if loadErr != nil {
		return reservationv1.Result{
			Schema: reservationv1.Schema, RepoID: repoID, Unknown: true,
			Detail: fmt.Sprintf("refresh failed (%v) and the prior artifact is corrupt (%v)", refreshErr, loadErr),
		}
	}
	if !ok {
		return reservationv1.Result{Schema: reservationv1.Schema, RepoID: repoID, Unknown: true, Detail: refreshErr.Error()}
	}
	return reservationv1.Result{
		Schema:       reservationv1.Schema,
		RepoID:       repoID,
		Reservations: prev.Reservations,
		Stale:        true,
		AsOf:         prev.AsOf,
		Generation:   strconv.FormatInt(prev.Generation, 10),
		Detail:       refreshErr.Error(),
	}
}

// queryLiveLocks asks the repository itself for every currently held lock,
// per r680: `svn info -r HEAD --xml --depth infinity -- file://<repo>@`.
// No working copy is checked out or maintained; svn info answers directly
// against the repository URL. FSFS internals are never read directly.
func queryLiveLocks(ctx context.Context, svnBinary, repoPath string) ([]reservationv1.Reservation, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	// url.URL.String() percent-encodes spaces and other characters a naive
	// "file://"+path concatenation would pass through unescaped, breaking
	// svn's own URL parsing for any repositories root containing them.
	// The trailing "@" is SVN's own separate peg-revision escape (applied
	// after URL construction, never encoded): without it, a repository
	// path that itself contains "@" would be misparsed as a peg-revision
	// specifier by svn's own argument handling.
	fileURL := (&url.URL{Scheme: "file", Path: filepath.ToSlash(repoPath)}).String() + "@"
	cmd := exec.CommandContext(ctx, svnBinary, "info", "-r", "HEAD", "--xml", "--depth", "infinity", "--", fileURL)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("svn info stdout pipe: %w", err)
	}
	var stderr limitedStderr
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("svn info start: %w", err)
	}
	reservations, parseErr := parseLockXML(stdout)
	waitErr := cmd.Wait()
	if waitErr != nil {
		return nil, fmt.Errorf("svn info: %w: %s", waitErr, stderr.String())
	}
	if parseErr != nil {
		return nil, fmt.Errorf("svn info xml: %w", parseErr)
	}
	return reservations, nil
}

type lockXMLEntry struct {
	RelativeURL string `xml:"relative-url"`
	Lock        *struct {
		Token   string `xml:"token"`
		Owner   string `xml:"owner"`
		Comment string `xml:"comment"`
		Created string `xml:"created"`
	} `xml:"lock"`
}

// parseLockXML reads svn info --xml streamingly (one <entry> decoded and
// discarded at a time via decoder.DecodeElement, never the whole document
// held in memory at once) and keeps only entries carrying a <lock> — most
// entries in a `--depth infinity` walk are ordinary unlocked paths.
func parseLockXML(r io.Reader) ([]reservationv1.Reservation, error) {
	decoder := xml.NewDecoder(r)
	var reservations []reservationv1.Reservation
	for {
		tok, err := decoder.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "entry" {
			continue
		}
		var entry lockXMLEntry
		if err := decoder.DecodeElement(&entry, &start); err != nil {
			return nil, err
		}
		if entry.Lock == nil {
			continue
		}
		reservations = append(reservations, reservationv1.Reservation{
			Path:      strings.TrimPrefix(entry.RelativeURL, "^/"),
			Token:     entry.Lock.Token,
			OwnerID:   entry.Lock.Owner,
			Comment:   entry.Lock.Comment,
			CreatedAt: entry.Lock.Created,
		})
	}
	return reservations, nil
}

type limitedStderr struct{ buf bytes.Buffer }

func (w *limitedStderr) Write(raw []byte) (int, error) {
	original := len(raw)
	remaining := reservationWorkerMaxStderr - w.buf.Len()
	if remaining > 0 {
		if len(raw) > remaining {
			raw = raw[:remaining]
		}
		w.buf.Write(raw)
	}
	return original, nil
}

func (w *limitedStderr) String() string { return w.buf.String() }
