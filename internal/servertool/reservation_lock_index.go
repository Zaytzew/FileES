package servertool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	reservationv1 "filees/pkg/reservation/v1"
)

// Read SVN's lock index, not every versioned file. This is still a live source
// check: admin unlock, auto-unlock and token replacement without a commit are
// visible without depending on incomplete producer hooks or an invented TTL.
func queryLockIndex(ctx context.Context, admin, repoPath string) ([]reservationv1.Reservation, error) {
	if !filepath.IsAbs(admin) || !filepath.IsAbs(repoPath) {
		return nil, errors.New("lock index requires absolute configured paths")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, admin, "lslocks", repoPath)
	cmd.Dir = repoPath
	// English grammar, UTF-8 paths/comments; never parse translated headings.
	cmd.Env = append(os.Environ(), "LC_ALL=", "LC_MESSAGES=C", "LC_CTYPE=C.UTF-8", "TZ=UTC")
	var out lockIndexOutput
	var stderr limitedStderr
	cmd.Stdout, cmd.Stderr = &out, &stderr
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("SVN lock index: %w: %s", err, stderr.String())
	}
	return parseLockIndex(out.data)
}

type lockIndexOutput struct{ data []byte }

func (b *lockIndexOutput) Write(p []byte) (int, error) {
	if len(b.data)+len(p) > 8<<20 {
		return 0, errors.New("SVN lock index exceeds 8 MiB")
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

func parseLockIndex(raw []byte) ([]reservationv1.Reservation, error) {
	if !utf8.Valid(raw) || len(raw) > 8<<20 {
		return nil, errors.New("invalid SVN lock index encoding or size")
	}
	// This parser is used for Unix svnadmin output only. Do not normalize CRLF:
	// CR can be part of a path, and normalization would silently change its identity.
	if strings.ContainsAny(string(raw), "\r\x00") {
		return nil, errors.New("ambiguous control character in SVN lock index")
	}
	lines := strings.Split(string(raw), "\n")
	result := []reservationv1.Reservation{}
	seen := map[string]bool{}
	bad := errors.New("incomplete or ambiguous SVN lock index")
	for i := 0; i < len(lines); {
		if lines[i] == "" {
			i++
			continue
		}
		values := make([]string, 6)
		for n, prefix := range []string{"Path: ", "UUID Token: ", "Owner: ", "Created: ", "Expires: ", "Comment ("} {
			if i >= len(lines) || !strings.HasPrefix(lines[i], prefix) {
				return nil, bad
			}
			values[n] = strings.TrimPrefix(lines[i], prefix)
			i++
		}
		path := strings.TrimPrefix(values[0], "/")
		if path == values[0] || path == "" || seen[path] || values[1] == "" || values[2] == "" {
			return nil, bad
		}
		for _, part := range strings.Split(path, "/") {
			if part == "" || part == "." || part == ".." {
				return nil, bad
			}
		}
		created, _, _ := strings.Cut(values[3], " (")
		when, err := time.Parse("2006-01-02 15:04:05 -0700", created)
		if err != nil {
			return nil, bad
		}
		header := strings.Fields(strings.TrimSuffix(values[5], "):"))
		if !strings.HasSuffix(values[5], "):") || len(header) != 2 || (header[1] != "line" && header[1] != "lines") {
			return nil, bad
		}
		count, err := strconv.Atoi(header[0])
		if err != nil || count < 0 || count > len(lines)-i {
			return nil, bad
		}
		comment := strings.Join(lines[i:i+count], "\n")
		i += count
		// The command separates records by an empty line. Without it a
		// truncated comment must not masquerade as another complete record.
		if i >= len(lines) || lines[i] != "" {
			return nil, bad
		}
		seen[path] = true
		result = append(result, reservationv1.Reservation{Path: path, Token: values[1], OwnerID: values[2], Comment: comment, CreatedAt: when.UTC().Format(time.RFC3339Nano)})
	}
	return result, nil
}
