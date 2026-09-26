package main

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"filees/pkg/alertchannel"
	"filees/pkg/client"
	"filees/pkg/clientview"
	contract "filees/pkg/contract/v1"
	"filees/pkg/runtime"
	"filees/pkg/talk"
	"github.com/google/uuid"
)

type serverAlertSources struct {
	mu     sync.Mutex
	boxes  map[string]*alertchannel.Inbox
	owners map[string]chan struct{}
}

func (s *serverAlertSources) Notices() ([]contract.Notice, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []contract.Notice
	for _, b := range s.boxes {
		rows, err := b.Notices()
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}
func (s *serverAlertSources) Ack(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.boxes {
		if err := b.Ack(id); err != nil {
			return err
		}
	}
	return nil
}

// mailboxURL is derived from the existing authenticated service URL, never
// from an arbitrary network endpoint in an alert payload.
func mailboxURL(serviceURL, clientID, realmID string) (string, error) {
	if _, err := uuid.Parse(realmID); err != nil {
		return "", err
	}
	u, err := url.Parse(serviceURL)
	if err != nil {
		return "", err
	}
	if u.Scheme != "svn+ssh" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("invalid alert service URL")
	}
	suffix := "/clients/" + clientID
	base := strings.TrimSuffix(u.Path, "/")
	if strings.HasSuffix(base, suffix) {
		base = strings.TrimSuffix(base, suffix)
	} else if base != "" {
		return "", errors.New("unrecognised service URL layout")
	}
	u.Path = path.Join(base, "alerts", realmID)
	u.RawPath = ""
	return u.String(), nil
}

type alertReader interface {
	Revision(context.Context, string) (int64, error)
	HistoryList(context.Context, string, int64) ([]client.HistoryEntry, error)
	HistoryFetchFile(context.Context, string, int64, string) (int64, error)
}

func readAlertSnapshot(ctx context.Context, r alertReader, mailbox, realm, tempRoot string) (alertchannel.Snapshot, error) {
	var empty alertchannel.Snapshot
	revision, err := r.Revision(ctx, mailbox)
	if err != nil {
		return empty, err
	}
	entries, err := r.HistoryList(ctx, mailbox, revision)
	if err != nil {
		return empty, err
	}
	found := false
	for _, v := range entries {
		if v.Name == "snapshot.json" {
			if v.Kind != "file" || v.Size < 0 || v.Size > alertchannel.MaxBytes {
				return empty, errors.New("invalid alert snapshot size")
			}
			found = true
		}
	}
	if !found {
		return empty, errors.New("missing alert snapshot")
	}
	dir, err := os.MkdirTemp(tempRoot, ".alert-read-")
	if err != nil {
		return empty, err
	}
	defer os.RemoveAll(dir)
	dest := filepath.Join(dir, "snapshot.json")
	n, err := r.HistoryFetchFile(ctx, mailbox+"/snapshot.json", revision, dest)
	if err != nil {
		return empty, err
	}
	if n > alertchannel.MaxBytes {
		return empty, errors.New("alert snapshot too large")
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		return empty, err
	}
	return alertchannel.Decode(data, realm)
}

// start owns a separate bounded lane. Projection sync and project commits never
// wait on alert reads. On cancellation its notices disappear from the source.
func (s *serverAlertSources) start(ctx context.Context, reader alertReader, server, clientID, serviceURL, cachePath string) func(clientview.View) {
	owner := make(chan struct{})
	s.mu.Lock()
	if s.owners == nil {
		s.owners = map[string]chan struct{}{}
	}
	s.owners[server] = owner
	delete(s.boxes, server)
	s.mu.Unlock()
	views := make(chan clientview.View, 1)
	runtime.Go(ctx, func() {
		var box *alertchannel.Inbox
		realm, mailbox := "", ""
		delay := time.Minute
		timer := time.NewTimer(time.Hour)
		defer timer.Stop()
		defer func() {
			s.mu.Lock()
			if s.owners[server] == owner {
				delete(s.owners, server)
				delete(s.boxes, server)
			}
			s.mu.Unlock()
		}()
		poll := func() {
			if box == nil {
				return
			}
			req, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			snap, err := readAlertSnapshot(req, reader, mailbox, realm, filepath.Dir(cachePath))
			if err == nil {
				err = box.Accept(snap, time.Now())
			}
			if err != nil {
				box.Failed()
				delay *= 2
				if delay > 5*time.Minute {
					delay = 5 * time.Minute
				}
			} else {
				delay = time.Minute
			}
			timer.Reset(delay)
		}
		for {
			select {
			case <-ctx.Done():
				return
			case view := <-views:
				if view.ClientID != clientID {
					continue
				}
				if view.RealmID == realm {
					continue
				}
				nextURL, err := mailboxURL(serviceURL, clientID, view.RealmID)
				if err != nil {
					continue
				}
				segment, err := serverPathSegment(view.RealmID)
				if err != nil {
					continue
				}
				next, err := alertchannel.Open(filepath.Join(filepath.Dir(cachePath), "alerts-"+segment+".json"), server, view.RealmID)
				if err != nil {
					talk.With("alerts:"+server).Warnf("cannot restore local alert inbox: %v", err)
					if next == nil {
						continue
					}
				}
				realm, mailbox, box = view.RealmID, nextURL, next
				s.mu.Lock()
				if s.boxes == nil {
					s.boxes = map[string]*alertchannel.Inbox{}
				}
				if s.owners[server] != owner {
					s.mu.Unlock()
					return
				}
				s.boxes[server] = box
				s.mu.Unlock()
				poll()
			case <-timer.C:
				poll()
			}
		}
	})
	return func(v clientview.View) {
		select {
		case views <- v:
		default:
		}
	}
}
