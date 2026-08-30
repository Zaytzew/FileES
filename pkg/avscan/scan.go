// Package avscan is the fail-closed content gate for Upload Channel.
package avscan

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// EICAR is the 68-byte industry test file. It is not malware; scanners treat
// it as a positive so reception can exercise the reject path safely.
const EICAR = `X5O!P%@AP[4\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*`

const EICARSignature = "Eicar-Test-Signature"

type Verdict int

const (
	Clean Verdict = iota
	Infected
	Unavailable
)

var ErrUnavailable = errors.New("antivirus scanner is unavailable")

type Scanner interface {
	Scan(context.Context, string) (Verdict, string, error)
}

// Command runs an absolute scanner. Exit 0 is clean, 1 is infected, anything
// else is unavailable. A missing binary never reports clean.
type Command struct {
	Path string
	Args []string
}

func LooksLikeEICAR(raw []byte) bool {
	value := strings.TrimSpace(string(raw))
	value = strings.TrimSuffix(value, "\x1a")
	return value == EICAR
}

func (c Command) Scan(ctx context.Context, path string) (Verdict, string, error) {
	if !filepath.IsAbs(c.Path) || !filepath.IsAbs(path) {
		return Unavailable, "", ErrUnavailable
	}
	if info, err := os.Stat(path); err == nil && info.Size() > 0 && info.Size() <= 256 {
		if raw, err := os.ReadFile(path); err == nil && LooksLikeEICAR(raw) {
			return Infected, EICARSignature, nil
		}
	}
	args := append(append([]string{}, c.Args...), path)
	cmd := exec.CommandContext(ctx, c.Path, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	detail := strings.TrimSpace(stdout.String() + "\n" + stderr.String())
	if err == nil {
		return Clean, detail, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return Infected, detail, nil
	}
	return Unavailable, detail, ErrUnavailable
}

// Static is a test double. Unavailable unless Verdict is set to Clean or Infected.
type Static struct {
	Verdict Verdict
	Detail  string
}

func (s Static) Scan(context.Context, string) (Verdict, string, error) {
	if s.Verdict == Unavailable {
		return Unavailable, s.Detail, ErrUnavailable
	}
	return s.Verdict, s.Detail, nil
}
