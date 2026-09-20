package errmap

import (
	"errors"
	"testing"

	"filees/pkg/client"
	"filees/pkg/errcat"
)

// The exact stderr a client produced on 2026-09-18 once demo.filees.space had
// removed its realm: sshd refused the key, svn added E170013. The refusal is
// permanent and must not be reported - or handled - as an outage (register D2).
const removedRealmStderr = "komenda 'update' zakończyła się błędem: exit status 1\n" +
	"_filees-client@demo.filees.space: Permission denied (publickey).\n" +
	"svn: E170013: Unable to connect to a repository at URL 'svn+ssh://_filees-client@demo.filees.space/clients/d8d27809-f088-4b6a-a699-37fb503eae5e'\n" +
	"svn: E210002: Network connection closed unexpectedly"

func TestARefusedKeyIsAnIdentityRefusalNotAnOutage(t *testing.T) {
	err := errors.New(removedRealmStderr)
	if got := Classify(err); got.Key != errcat.KeyAuthFailed {
		t.Fatalf("refused key classified as %s, want %s", got.Key, errcat.KeyAuthFailed)
	}
	if client.IsNetworkError(err) {
		t.Fatal("a refused key must not put the commit service offline")
	}
	if !client.IsIdentityRefused(err) {
		t.Fatal("the refusal was not recognised")
	}

	// The OpenSSH variant listing more methods is the same answer.
	if !client.IsIdentityRefused(errors.New("host: Permission denied (publickey,password).")) {
		t.Fatal("multi-method refusal was not recognised")
	}

	// A plain connection failure stays a network condition.
	outage := errors.New("svn: E170013: Unable to connect to a repository at URL 'svn+ssh://x'\nssh: connect to host x port 22: Connection refused")
	if !client.IsNetworkError(outage) || Classify(outage).Key != errcat.KeyNetUnreachable {
		t.Fatalf("outage lost its network classification: %s", Classify(outage).Key)
	}
}

func TestRefusedActivationProofIsNotAnOutage(t *testing.T) {
	err := errors.New("filees-client-entry: proof does not match one live staged or active client\nsvn: E170013: Unable to connect to a repository at URL 'svn+ssh://host/repo'\nsvn: E210002: Network connection closed unexpectedly")
	if got := Classify(err); got.Key != errcat.KeyAuthFailed || got.IsNetwork() {
		t.Fatalf("proof refusal classified as %+v", got)
	}
	if !client.IsIdentityRefused(err) || client.IsNetworkError(err) {
		t.Fatal("proof refusal must not enter the network retry path")
	}
}
