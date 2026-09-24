package client

import (
	"bytes"
	"context"
	"io"
	"strconv"
	"sync"
)

// CommitProgress is what a running native commit has done so far: files whose
// content has been sent, and bytes the RA session has moved (protocol
// included, so it is a measure of motion, not an exact share of the payload).
type CommitProgress struct {
	FilesDone int
	BytesSent int64
}

type commitProgressKey struct{}

// WithCommitProgress asks the native commit started with ctx to report while
// it runs (filees-svn commit --progress, feature commit_progress_v1). A
// helper without the feature commits exactly as before and reports nothing.
func WithCommitProgress(ctx context.Context, report func(CommitProgress)) context.Context {
	if report == nil {
		return ctx
	}
	return context.WithValue(ctx, commitProgressKey{}, report)
}

func commitProgressFrom(ctx context.Context) func(CommitProgress) {
	report, _ := ctx.Value(commitProgressKey{}).(func(CommitProgress))
	return report
}

const progressPrefix = "filees-progress\t"

// progressSplitter takes the helper's stderr apart: "filees-progress" lines
// become reports, every other byte goes on to the diagnostic buffer, which
// must never see a progress line (it is what an error message is built from).
type progressSplitter struct {
	mu      sync.Mutex
	report  func(CommitProgress)
	inner   io.Writer
	partial []byte
	state   CommitProgress
}

func (s *progressSplitter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.partial = append(s.partial, p...)
	for {
		i := bytes.IndexByte(s.partial, '\n')
		if i < 0 {
			break
		}
		line := s.partial[:i+1]
		s.partial = s.partial[i+1:]
		if !s.take(line) {
			if _, err := s.inner.Write(line); err != nil {
				return len(p), err
			}
		}
	}
	return len(p), nil
}

// flush hands an unterminated last line to the diagnostic buffer.
func (s *progressSplitter) flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.partial) > 0 && !s.take(append(s.partial, '\n')) {
		_, _ = s.inner.Write(s.partial)
	}
	s.partial = nil
}

func (s *progressSplitter) take(line []byte) bool {
	text := string(bytes.TrimRight(line, "\r\n"))
	if len(text) < len(progressPrefix) || text[:len(progressPrefix)] != progressPrefix {
		return false
	}
	fields := bytes.Split([]byte(text[len(progressPrefix):]), []byte("\t"))
	switch string(fields[0]) {
	case "file":
		s.state.FilesDone++
	case "bytes":
		if len(fields) != 2 {
			return true
		}
		n, err := strconv.ParseInt(string(fields[1]), 10, 64)
		if err != nil || n < s.state.BytesSent {
			return true
		}
		s.state.BytesSent = n
	default:
		return true
	}
	s.report(s.state)
	return true
}
