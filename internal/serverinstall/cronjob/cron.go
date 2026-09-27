// Package cronjob maintains only FileES's marked capacity entry in the state
// user's crontab. It does not replace unrelated jobs or alter cron's service.
package cronjob

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"time"
)

const Marker = "# filees-capacity-v1"
const User = "_filees-state"

func Line(admin, config string) (string, error) {
	quote := func(v string) (string, error) {
		if !strings.HasPrefix(v, "/") || strings.ContainsAny(v, "\r\n\x00%") {
			return "", errors.New("invalid capacity cron path")
		}
		return "'" + strings.ReplaceAll(v, "'", "'\"'\"'") + "'", nil
	}
	a, e := quote(admin)
	if e != nil {
		return "", e
	}
	c, e := quote(config)
	if e != nil {
		return "", e
	}
	return "* * * * * " + a + " -config " + c + " alert capacity --scheduled " + Marker, nil
}
func Merge(current, line string) string {
	rows := strings.Split(strings.TrimSuffix(current, "\n"), "\n")
	out := []string{}
	for _, r := range rows {
		if strings.HasSuffix(strings.TrimSpace(r), Marker) {
			continue
		}
		if len(rows) == 1 && r == "" {
			continue
		}
		out = append(out, r)
	}
	out = append(out, line)
	return strings.Join(out, "\n") + "\n"
}

type Plan struct {
	Program, Line string
	Before, After string
	Changed       bool
}

func command(ctx context.Context, program string, input string, args ...string) (string, error) {
	c := exec.CommandContext(ctx, program, args...)
	c.Env = append(os.Environ(), "LC_ALL=C")
	if input != "" {
		c.Stdin = strings.NewReader(input)
	}
	var out, stderr bytes.Buffer
	c.Stdout = &out
	c.Stderr = &stderr
	e := c.Run()
	if e != nil {
		return "", fmt.Errorf("crontab: %w: %s", e, strings.TrimSpace(stderr.String()))
	}
	return out.String(), nil
}
func read(ctx context.Context, program string) (string, error) {
	s, e := command(ctx, program, "", "-u", User, "-l")
	// Only the documented no-table outcome means empty. Permission errors,
	// missing commands and damaged spool must never erase an existing table.
	var exit *exec.ExitError
	if e != nil && errors.As(e, &exit) && exit.ExitCode() == 1 && strings.HasSuffix(e.Error(), "no crontab for "+User) {
		return "", nil
	}
	return s, e
}
func Prepare(ctx context.Context, admin, config string, firstInstall bool) (*Plan, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	line, e := Line(admin, config)
	if e != nil {
		return nil, e
	}
	bin, e := exec.LookPath("crontab")
	if e != nil {
		return nil, fmt.Errorf("capacity scheduler requires crontab (install and enable cron/crond): %w", e)
	}
	current := ""
	_, lookupErr := user.Lookup(User)
	if lookupErr == nil || !firstInstall {
		current, e = read(ctx, bin)
		if e != nil {
			return nil, e
		}
	} else {
		var unknown user.UnknownUserError
		if !errors.As(lookupErr, &unknown) {
			return nil, lookupErr
		}
	}
	next := Merge(current, line)
	return &Plan{bin, line, current, next, current != next}, nil
}
func (p *Plan) Apply(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// Reread at the point of mutation so edits made during payload staging are
	// retained. crontab itself provides no compare-and-swap API.
	current, e := read(ctx, p.Program)
	if e != nil {
		return e
	}
	next := Merge(current, p.Line)
	if next == current {
		return nil
	}
	if _, e = command(ctx, p.Program, next, "-u", User, "-"); e != nil {
		return e
	}
	actual, e := read(ctx, p.Program)
	if e != nil {
		return e
	}
	if actual != next {
		return errors.New("capacity crontab verification failed")
	}
	return nil
}
func Ensure(ctx context.Context, admin, config string) error {
	p, e := Prepare(ctx, admin, config, false)
	if e != nil {
		return e
	}
	return p.Apply(ctx)
}
