package releaseenvelope

import (
	"context"
	"strings"
	"testing"
)

// These values are from OpenBSD's regress/usr.bin/signify test suite.
const (
	openBSDRegressPublicKey = "untrusted comment: signify public key\nRWTAeKJJ1MTF3YCo0ivtKH8kuiFWJuLpNoUmpDd6iTFYhn6/tRu5qKJe\n"
	openBSDRegressSignature = "untrusted comment: signature from signify secret key\nRWTAeKJJ1MTF3UpxzBCu6NaM6HPJNTj5CZ+M5XNJKNeEHBLQSsstzHGbSo8rPYNgw3Z98pN7WKiIwBIyRrKuIdKBRA6qlaci6wI=\n"
	openBSDRegressMessage   = "Attack at dawn!\n"
)

func TestEd25519VerifierAcceptsOpenBSDSignifyFixture(t *testing.T) {
	verifier := Ed25519Verifier{Keys: map[string][]byte{"release-1": []byte(openBSDRegressPublicKey)}}
	if err := verifier.Verify(context.Background(), "release-1", []byte(openBSDRegressMessage), []byte(openBSDRegressSignature)); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalSignifyPublicKeyRepairsOnlyTransportLineEndings(t *testing.T) {
	for _, input := range []string{
		openBSDRegressPublicKey,
		strings.ReplaceAll(openBSDRegressPublicKey, "\n", "\r\n"),
		strings.TrimSuffix(openBSDRegressPublicKey, "\n"),
	} {
		got, err := CanonicalSignifyPublicKey([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != openBSDRegressPublicKey {
			t.Fatalf("canonical key = %q", got)
		}
	}
	for _, input := range []string{
		strings.Replace(openBSDRegressPublicKey, "\n", "\r", 1),
		openBSDRegressPublicKey + "\n",
		"untrusted comment: key\nnot-base64\n",
	} {
		if _, err := CanonicalSignifyPublicKey([]byte(input)); err == nil {
			t.Fatalf("accepted malformed public key %q", input)
		}
	}
}

func TestEd25519VerifierFailsClosed(t *testing.T) {
	verifier := Ed25519Verifier{Keys: map[string][]byte{"release-1": []byte(openBSDRegressPublicKey)}}
	tests := []struct {
		name, key, message, signature string
	}{
		{"unknown key", "release-2", openBSDRegressMessage, openBSDRegressSignature},
		{"changed message", "release-1", "Retreat at dawn!\n", openBSDRegressSignature},
		{"changed signature", "release-1", openBSDRegressMessage, strings.Replace(openBSDRegressSignature, "RWTA", "RWTC", 1)},
		{"trailing content", "release-1", openBSDRegressMessage, openBSDRegressSignature + "ignored\n"},
		{"missing comment", "release-1", openBSDRegressMessage, strings.Split(openBSDRegressSignature, "\n")[1] + "\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := verifier.Verify(context.Background(), test.key, []byte(test.message), []byte(test.signature)); err == nil {
				t.Fatal("verification unexpectedly succeeded")
			}
		})
	}
}
