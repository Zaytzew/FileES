package client

import (
	"context"
	"encoding/xml"
	"errors"
	"path/filepath"
	"strconv"
)

// LockObservation is a fresh, target-bound server observation, not an outcome
// receipt for an in-flight mutation. No WC-token fallback for Remote.
type LockObservation struct{ Local, Remote *LockInfo }
type LockObservationReader interface {
	ReadLockObservation(context.Context, string, string) (LockObservation, error)
}

func (c *execClient) ReadLockObservation(ctx context.Context, wc, path string) (LockObservation, error) {
	if nativeWCOps(c) {
		return c.nativeReadLockObservation(ctx, wc, path)
	}
	args := append([]string{"status", "--xml", "--verbose", "--show-updates", "--depth", "empty"}, c.pathArgs(wc, []string{path})...)
	out, err := c.run(ctx, wc, args)
	if err != nil {
		return LockObservation{}, err
	}
	return parseLockObservation(out, wc, path)
}

func parseLockObservation(raw, wc, path string) (LockObservation, error) {
	var doc struct {
		XMLName xml.Name `xml:"status"`
		Targets []struct {
			Against []struct {
				Revision string `xml:"revision,attr"`
			} `xml:"against"`
			Entries []struct {
				Path string `xml:"path,attr"`
				WC   struct {
					Item string   `xml:"item,attr"`
					Lock *lockXML `xml:"lock"`
				} `xml:"wc-status"`
				Repos struct {
					Lock *lockXML `xml:"lock"`
				} `xml:"repos-status"`
			} `xml:"entry"`
		} `xml:"target"`
	}
	if err := xml.Unmarshal([]byte(raw), &doc); err != nil {
		return LockObservation{}, err
	}
	invalid := errors.New("lock observation requires one versioned target and server revision")
	if len(doc.Targets) != 1 || len(doc.Targets[0].Entries) != 1 || len(doc.Targets[0].Against) != 1 {
		return LockObservation{}, invalid
	}
	target := doc.Targets[0]
	if n, err := strconv.ParseInt(target.Against[0].Revision, 10, 64); err != nil || n < 0 {
		return LockObservation{}, invalid
	}
	e := target.Entries[0]
	switch e.WC.Item {
	case "normal", "modified", "missing", "deleted", "replaced", "conflicted":
	default:
		return LockObservation{}, invalid
	}
	if e.Path == "" {
		return LockObservation{}, invalid
	}
	absolute := func(p string) string {
		if !filepath.IsAbs(p) {
			p = filepath.Join(wc, filepath.FromSlash(p))
		}
		return filepath.Clean(p)
	}
	if absolute(e.Path) != absolute(path) {
		return LockObservation{}, errors.New("lock observation target mismatch")
	}
	convert := func(lock *lockXML) (*LockInfo, error) {
		if lock == nil {
			return nil, nil
		}
		if lock.Token == "" || lock.Owner == "" {
			return nil, errors.New("incomplete observed lock")
		}
		return &LockInfo{Token: lock.Token, Owner: lock.Owner, Comment: lock.Comment}, nil
	}
	local, err := convert(e.WC.Lock)
	if err != nil {
		return LockObservation{}, err
	}
	remote, err := convert(e.Repos.Lock)
	if err != nil {
		return LockObservation{}, err
	}
	return LockObservation{Local: local, Remote: remote}, nil
}
