package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
)

// ProjectionRemoval is emitted only from a native SVN file-remove work item.
// The parent must bind this request to its current anchor and exact FileID,
// persist reprojection evidence, and refuse local/dirty/resident objects.
type ProjectionRemoval struct {
	Schema    string `json:"schema"`
	Operation string `json:"operation"`
	Sequence  uint32 `json:"sequence"`
	Root      string `json:"root"`
	Path      string `json:"path"`
	FileID    string `json:"file_id"`
}

// LocalProjectionCompleter restores the virtual view under the service writer,
// before its pending fence is cleared or a scanner is allowed to checkpoint.
type LocalProjectionCompleter interface {
	CompleteWorkingCopyProjection(context.Context, string) error
}

func (c *execClient) CompleteWorkingCopyProjection(ctx context.Context, wc string) error {
	if c.projectionComplete == nil {
		return nil
	}
	return c.projectionComplete(ctx, wc)
}

type projectionSplitter struct {
	ctx      context.Context
	root     string
	token    string
	sequence uint32
	pending  []byte
	inner    io.Writer
	ack      io.WriteCloser
	remove   func(context.Context, ProjectionRemoval) error
	failure  error
	abort    func()
}

func (p *projectionSplitter) Write(data []byte) (int, error) {
	count := len(data)
	if p.failure != nil {
		return 0, p.failure
	}
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		chunk := data
		if i >= 0 {
			chunk = data[:i+1]
		}
		if len(p.pending)+len(chunk) > nativeListingLimit {
			return 0, p.fail(errors.New("projection receipt exceeds limit"))
		}
		p.pending = append(p.pending, chunk...)
		data = data[len(chunk):]
		if i >= 0 {
			if err := p.line(p.pending); err != nil {
				return 0, p.fail(err)
			}
			p.pending = nil
		}
	}
	return count, nil
}

func (p *projectionSplitter) fail(err error) error {
	p.failure = err
	_ = p.ack.Close() // Wake the child on refusal, without an affirmative ACK.
	if p.abort != nil {
		p.abort()
	} // Do not let a long diagnostic fill an unread pipe.
	return err
}

func (p *projectionSplitter) line(line []byte) error {
	var envelope struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(line, &envelope); err != nil {
		return err
	}
	if envelope.Schema != "filees.svn-projection/v1" {
		_, err := p.inner.Write(line)
		return err
	}
	if len(line) > 64<<10 {
		return errors.New("projection request exceeds limit")
	}
	var request ProjectionRemoval
	d := json.NewDecoder(bytes.NewReader(line))
	d.DisallowUnknownFields()
	if err := d.Decode(&request); err != nil {
		return err
	}
	if request.Operation != p.token || request.Sequence != p.sequence+1 || p.sequence >= 65536 {
		return errors.New("projection operation or sequence differs")
	}
	if !filepath.IsAbs(request.Root) || !strings.EqualFold(filepath.Clean(request.Root), filepath.Clean(p.root)) {
		return errors.New("projection root differs")
	}
	if !validMovePath(request.Path) {
		return errors.New("projection path is not valid")
	}
	if !projectionFileID(request.FileID) {
		return fmt.Errorf("projection has invalid FileID %q", request.FileID)
	}
	if err := p.ctx.Err(); err != nil {
		return err
	}
	if err := p.remove(p.ctx, request); err != nil {
		return fmt.Errorf("physical SVN projection: %w", err)
	}
	if err := p.ctx.Err(); err != nil {
		return err
	}
	_, err := fmt.Fprintf(p.ack, "projection-ok %s %d\n", p.token, request.Sequence)
	if err == nil {
		p.sequence = request.Sequence
	}
	return err
}

func projectionFileID(value string) bool {
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return false
	}
	for i, part := range parts {
		bits := 64
		if i == 0 {
			bits = 32
		}
		n, err := strconv.ParseUint(part, 10, bits)
		if err != nil || strconv.FormatUint(n, 10) != part || (i == 1 && n == 0) {
			return false
		}
	}
	return true
}

func (p *projectionSplitter) flush() error {
	if p.failure != nil {
		return p.failure
	}
	if len(p.pending) == 0 {
		return nil
	}
	err := p.line(p.pending)
	p.pending = nil
	return err
}
