package releaseenvelope

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"filees/internal/serverinstall/signify"
)

type SignifyVerifier struct {
	Keys     map[string][]byte
	Verifier signify.Verifier
	TempRoot string
}

func (verifier SignifyVerifier) Verify(ctx context.Context, keyID string, message, signature []byte) error {
	keyID = strings.TrimSpace(keyID)
	publicKey, ok := verifier.Keys[keyID]
	if !ok || len(publicKey) == 0 {
		return fmt.Errorf("release key %q is not trusted", keyID)
	}
	if verifier.Verifier == nil {
		return errors.New("signify verifier is not configured")
	}
	root := strings.TrimSpace(verifier.TempRoot)
	if root == "" {
		root = os.TempDir()
	}
	temp, err := os.MkdirTemp(root, "filees-release-verify-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	paths := map[string][]byte{
		filepath.Join(temp, "release.pub"): publicKey,
		filepath.Join(temp, "message"):     message,
		filepath.Join(temp, "message.sig"): signature,
	}
	for path, data := range paths {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return err
		}
	}
	return verifier.Verifier.Verify(ctx, filepath.Join(temp, "release.pub"), filepath.Join(temp, "message"), filepath.Join(temp, "message.sig"))
}
