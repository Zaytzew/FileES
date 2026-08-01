package localpin

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
)

// encryptPIN encrypts pin under the primary (most-preferred) device key.
func encryptPIN(path string, pin []byte) (string, error) {
	keys := deviceKeys(path)
	gcm, err := newGCM(keys[0])
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, pin, nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// decryptPIN tries every derived device key in turn (most-preferred first),
// so a username change alone does not strand an otherwise-unchanged
// machine/account - mirrors syschat's decryptPassword fallback loop.
// usedKey is exactly the deviceKeys entry that decrypted successfully, so a
// caller can compare it against the current primary key and detect that a
// legacy (e.g. MAC-derived) fallback was needed.
func decryptPIN(path, encoded string) (plaintext, usedKey []byte, err error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, nil, err
	}
	var lastErr error
	for _, key := range deviceKeys(path) {
		gcm, err := newGCM(key)
		if err != nil {
			return nil, nil, err
		}
		nonceSize := gcm.NonceSize()
		if len(raw) < nonceSize {
			lastErr = errors.New("encrypted PIN too short")
			continue
		}
		pt, err := gcm.Open(nil, raw[:nonceSize], raw[nonceSize:], nil)
		if err == nil {
			return pt, key, nil
		}
		lastErr = err
	}
	return nil, nil, lastErr
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
