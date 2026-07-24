package main

import (
	"encoding/json"
	"errors"
	"io"
	"time"
)

// handoff is the one JSON line the spawning filees-gui process writes to
// this process's stdin (cmd/filees-gui/run.go's mobilePairingAdapter),
// then immediately closes stdin - a single write, single read, invisible
// to argv/env/ps, following the same discipline pkg/deploy/tunnel_linux.go
// uses for the FIFO-based askpass handoff (here via os/exec's own private
// pipe instead, since the parent spawns this process directly).
type handoff struct {
	Address       string `json:"address"`
	HostPublicKey string `json:"host_public_key"`
	Token         string `json:"token"`
	ExpiresAt     string `json:"expires_at"`
}

// qrPayload is exactly the JSON schema the already-shipped Android client
// expects (MainActivity.kt / androidbind.PairJSON) - field names are fixed,
// do not rename.
type qrPayload struct {
	Address       string `json:"address"`
	HostPublicKey string `json:"host_public_key"`
	Token         string `json:"token"`
}

func (h handoff) qrJSON() ([]byte, error) {
	return json.Marshal(qrPayload{Address: h.Address, HostPublicKey: h.HostPublicKey, Token: h.Token})
}

func (h handoff) expiry() (time.Time, error) {
	return time.Parse(time.RFC3339Nano, h.ExpiresAt)
}

func readHandoff(r io.Reader) (handoff, error) {
	var h handoff
	dec := json.NewDecoder(io.LimitReader(r, 1<<16))
	if err := dec.Decode(&h); err != nil {
		return handoff{}, err
	}
	if h.Address == "" || h.HostPublicKey == "" || h.Token == "" {
		return handoff{}, errors.New("incomplete pairing handoff payload")
	}
	return h, nil
}
