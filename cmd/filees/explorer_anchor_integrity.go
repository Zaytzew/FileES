//go:build !nocfapi

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	"filees/pkg/commit"
	"filees/pkg/localrepo"
)

// The durable pending reference is installed BEFORE a source path is handed
// to Windows. Even a later nocfapi build must not publish an unverified file.
func (m *anchorManager) materializeTransfer(ctx context.Context, record localrepo.Record, identity string, revision, size int64, cache *anchorFetchCache) (string, int64, error) {
	file, revision, err := m.materializeCached(ctx, record, identity, revision, size, cache)
	if err != nil {
		return "", 0, err
	}
	state, err := m.integrityState(ctx, record, identity)
	if err != nil {
		return "", 0, err
	}
	if state.Local {
		proof, e := commit.ReadAnchorIntegrity(record.LocalPath, mustAnchorPath(identity))
		if e == nil && proof.Phase == "verified" && proof.FileID == state.FileID {
			return file, revision, nil
		}
		return "", 0, commit.AnchorIntegrityError(mustAnchorPath(identity), errors.New("unverified local object during fetch"))
	}
	_, err = m.bindIntegrityReference(record, identity, revision, state, file)
	return file, revision, err
}

func mustAnchorPath(identity string) string {
	rel, _, _, _ := parseAnchorRemoteIdentity(identity)
	return rel
}

func (m *anchorManager) integrityState(ctx context.Context, record localrepo.Record, identity string) (cfapiAnswer, error) {
	rel, _, _, valid := parseAnchorRemoteIdentity(identity)
	if !valid {
		return cfapiAnswer{}, errors.New("invalid integrity identity")
	}
	answer, err := m.call(ctx, rel+"\n", "file-states", "--root", record.LocalPath)
	if err != nil {
		return cfapiAnswer{}, err
	}
	if !answer.OK || len(answer.States) != 1 || !answer.States[0].OK {
		return cfapiAnswer{}, commit.AnchorIntegrityError(rel, errors.New("integrity metadata unavailable"))
	}
	state := answer.States[0]
	if !state.Placeholder || state.Directory || state.FileID == "" || state.USN == "" || state.Size == nil ||
		(!state.Local && state.Identity != identity) {
		return state, commit.AnchorIntegrityError(rel, errors.New("integrity object binding unavailable or changed"))
	}
	return state, nil
}

func integrityBindingMatches(r commit.AnchorIntegrityRecord, record localrepo.Record, identity string, revision int64, state cfapiAnswer) bool {
	return r.Path == mustAnchorPath(identity) && r.URL == record.RepoURL && r.Identity == identity &&
		r.Revision == revision && state.Size != nil && r.Size == *state.Size && r.FileID == state.FileID
}

func (m *anchorManager) bindIntegrityReference(record localrepo.Record, identity string, revision int64, state cfapiAnswer, source string) (commit.AnchorIntegrityRecord, error) {
	rel := mustAnchorPath(identity)
	r, err := commit.ReadAnchorIntegrity(record.LocalPath, rel)
	if err == nil && r.SHA256 != "" && integrityBindingMatches(r, record, identity, revision, state) {
		err = commit.BeginAnchorIntegrity(record.LocalPath, &r, state.Partial)
		return r, err
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return r, commit.AnchorIntegrityError(rel, err)
	}
	// Hash only the private, pinned materialization. Never read a partial
	// user placeholder in Go and never use its bytes as their own reference.
	file, err := anchorOpenMaterialization(source)
	if err != nil {
		return r, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || state.Size == nil || info.Size() != *state.Size {
		return r, commit.AnchorIntegrityError(rel, errors.New("reference size differs"))
	}
	hash := sha256.New()
	n, err := io.Copy(hash, file)
	if err != nil {
		return r, err
	}
	if n != *state.Size {
		return r, errors.New("reference changed while hashing")
	}
	r = commit.AnchorIntegrityRecord{Path: rel, URL: record.RepoURL, Identity: identity, FileID: state.FileID, Revision: revision, Size: n, SHA256: hex.EncodeToString(hash.Sum(nil))}
	err = commit.BeginAnchorIntegrity(record.LocalPath, &r, state.Partial)
	return r, err
}

// A positive native read-back is a proof of INITIAL delivery only. Once the
// same object has this proof, subsequent local edits must not be overwritten
// or compared against the old remote bytes as if they were transport errors.
func (m *anchorManager) verifyAnchorIntegrity(ctx context.Context, record localrepo.Record, identity string, revision int64) error {
	key := record.LocalPath + "\x00" + record.RepoURL + "\x00" + identity + "\x00" + strconv.FormatInt(revision, 10)
	m.integrityMu.Lock()
	if job := m.integrityJobs[key]; job != nil {
		m.integrityMu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-job.done:
			if errors.Is(job.err, context.Canceled) || errors.Is(job.err, context.DeadlineExceeded) {
				return commit.ErrLocalStatusBusy
			}
			return job.err
		}
	}
	job := &anchorIntegrityJob{done: make(chan struct{})}
	if m.integrityJobs == nil {
		m.integrityJobs = make(map[string]*anchorIntegrityJob)
	}
	m.integrityJobs[key] = job
	m.integrityMu.Unlock()
	job.err = m.verifyAnchorIntegrityOnce(ctx, record, identity, revision)
	m.integrityMu.Lock()
	delete(m.integrityJobs, key)
	close(job.done)
	m.integrityMu.Unlock()
	return job.err
}

type anchorIntegrityJob struct {
	done chan struct{}
	err  error
}

func (m *anchorManager) verifyAnchorIntegrityOnce(ctx context.Context, record localrepo.Record, identity string, revision int64) error {
	if err := m.anchorFetchAuthority(record); err != nil {
		return err
	}
	rel := mustAnchorPath(identity)
	state, err := m.integrityState(ctx, record, identity)
	if err != nil {
		return err
	}
	if state.Partial {
		return commit.AnchorIntegrityError(rel, errors.New("download is not fully resident"))
	}
	proof, readErr := commit.ReadAnchorIntegrity(record.LocalPath, rel)
	if readErr == nil && proof.Phase == "verified" && proof.Path == rel && proof.URL == record.RepoURL &&
		proof.Identity == identity && proof.Revision == revision && proof.FileID == state.FileID {
		return nil
	}
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return commit.AnchorIntegrityError(rel, readErr)
	}
	if state.Local {
		return commit.AnchorIntegrityError(rel, errors.New("local object lacks its initial delivery proof"))
	}
	if readErr != nil {
		proof = commit.AnchorIntegrityRecord{Path: rel, URL: record.RepoURL, Identity: identity, FileID: state.FileID, Revision: revision, Size: *state.Size}
		if err := commit.BeginAnchorIntegrity(record.LocalPath, &proof, false); err != nil {
			return commit.AnchorIntegrityError(rel, err)
		}
	}
	if readErr != nil || proof.SHA256 == "" || !integrityBindingMatches(proof, record, identity, revision, state) {
		source, pinned, err := m.materializeAt(ctx, record, identity, revision, *state.Size)
		if err != nil {
			return commit.AnchorIntegrityError(rel, err)
		}
		proof, err = m.bindIntegrityReference(record, identity, pinned, state, source)
		if err != nil {
			return commit.AnchorIntegrityError(rel, err)
		}
	}
	if err := m.anchorFetchAuthority(record); err != nil {
		return err
	}
	answer, verifyErr := m.call(ctx, "", "verify-download", "--root", record.LocalPath, "--rel", rel,
		"--identity", identity, "--file-id", state.FileID, "--size", strconv.FormatInt(proof.Size, 10), "--usn", state.USN, "--sha256", proof.SHA256)
	if ctx.Err() != nil {
		return ctx.Err()
	} // Interrupted read is no checksum verdict.
	if !answer.OK && (answer.HResult == "0x80070020" || answer.HResult == "0x800704d5") {
		// Sharing violation / changed metadata: leave the durable fence intact
		// and retry through existing adoption, never claim successful validation.
		return commit.ErrLocalStatusBusy
	}
	verified := verifyErr == nil && answer.OK && answer.Detail == "download_integrity_verified"
	reason := "native read-back unverified"
	if verifyErr != nil {
		reason = verifyErr.Error()
	} else if !verified {
		reason = fmt.Sprintf("%s (%s)", answer.Error, answer.HResult)
	}
	if err := commit.FinishAnchorIntegrity(record.LocalPath, proof, verified, reason); err != nil {
		return commit.AnchorIntegrityError(rel, err)
	}
	if !verified {
		return commit.AnchorIntegrityError(rel, errors.New(reason))
	}
	return nil
}
