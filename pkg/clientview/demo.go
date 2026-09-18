package clientview

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// DemoSchema is the demo server's announcement to one client: when its realm
// ends. It lives in its own file beside view.json, not inside it.
//
// view.json is decoded with DisallowUnknownFields, so a demo field there would
// make the whole projection unreadable to every client that predates it - and
// those are exactly the clients that already carry the Demo button (0.1.16.1352
// and later). A separate file is simply not read by them.
const DemoSchema = "filees.client-demo/v1"

// DemoFileName sits next to view.json in the client's service projection.
const DemoFileName = "demo.json"

// Demo is written once, at activation, by a server with a demo policy.
// ExpiresAt is activated_at plus the realm TTL: the same instant from which
// `filees-admin demo reap` counts, so the countdown and the removal agree.
type Demo struct {
	Schema    string    `json:"schema"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (d Demo) Validate() error {
	if d.Schema != DemoSchema {
		return fmt.Errorf("demo announcement schema %q", d.Schema)
	}
	if d.ExpiresAt.IsZero() {
		return errors.New("demo announcement has no expires_at")
	}
	return nil
}

// LoadDemo reads the announcement; found is false when the server published
// none, which is every server without a demo policy.
func LoadDemo(path string) (Demo, bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Demo{}, false, nil
	}
	if err != nil {
		return Demo{}, false, err
	}
	// Unknown fields are accepted on purpose, unlike view.json: this file
	// exists so a server can announce demo facts without breaking older
	// clients, and a later fact (the realm quota, say) must not break this one.
	decoder := json.NewDecoder(io.LimitReader(bytes.NewReader(raw), 4096))
	var demo Demo
	if err := decoder.Decode(&demo); err != nil {
		return Demo{}, false, fmt.Errorf("decode demo announcement: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Demo{}, false, errors.New("demo announcement contains trailing data")
	}
	if err := demo.Validate(); err != nil {
		return Demo{}, false, err
	}
	demo.ExpiresAt = demo.ExpiresAt.UTC()
	return demo, true, nil
}
