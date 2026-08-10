package main

import (
	"strings"
	"testing"

	"filees/internal/gui/actions"
	"filees/public-shares/gate"
)

func TestPublicShareDeclarationHashesPasswordBeforeContractAndGeneratesOpaqueID(t *testing.T) {
	declaration := actions.PublicShareDeclaration{
		RepoID: "repo-1", SourceRoot: "public", Slug: "wydanie",
		Password: []byte("bardzo-tajne"),
		Objects:  []actions.PublicShareObject{{RepoPath: "public/a.txt", DisplayName: "a.txt"}},
	}
	remote, err := publicShareDeclarationToContract(declaration)
	if err != nil {
		t.Fatal(err)
	}
	if remote.PasswordHash == "" || strings.Contains(remote.PasswordHash, "bardzo-tajne") {
		t.Fatalf("password crossed contract without verifier: %q", remote.PasswordHash)
	}
	if ok, err := gate.VerifyPassword(remote.PasswordHash, "bardzo-tajne"); err != nil || !ok {
		t.Fatalf("generated verifier rejected password: ok=%v err=%v", ok, err)
	}
	if len(remote.Objects) != 1 || len(remote.Objects[0].PublicID) != 32 {
		t.Fatalf("object map=%+v", remote.Objects)
	}
	for _, ch := range remote.Objects[0].PublicID {
		if ch < '0' || ch > '9' && (ch < 'a' || ch > 'z') {
			t.Fatalf("public id is not lowercase alphanumeric: %q", remote.Objects[0].PublicID)
		}
	}
}

func TestPublicShareDeclarationPreservesExistingOpaqueID(t *testing.T) {
	const publicID = "1234567890abcdef1234567890abcdef"
	remote, err := publicShareDeclarationToContract(actions.PublicShareDeclaration{Objects: []actions.PublicShareObject{{PublicID: publicID, RepoPath: "public/a.txt", DisplayName: "a.txt"}}})
	if err != nil {
		t.Fatal(err)
	}
	if remote.Objects[0].PublicID != publicID {
		t.Fatalf("public id=%q", remote.Objects[0].PublicID)
	}
}
