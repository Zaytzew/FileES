package localpin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

// deviceKeys derives one or more AES-256 keys tied to this installation and
// account, mirroring the technique already used and proven in the
// (separate, unrelated) syschat project's password-at-rest encryption
// (syschat/internal/config.go deriveEncryptionKeys/deriveEncryptionKeyMaterial):
// sha256(hostname + identity + path + a fixed salt + an extra binding
// value). None of hostname/uid/username/the extra value is ever persisted
// in the PIN record itself - the key exists only as a runtime derivation,
// so a stolen pin.json is worthless off-device: a PIN's entropy (4-6
// digits) is far too low for a password hash like bcrypt to meaningfully
// protect against offline brute force of a stolen hash, but an attacker
// without this exact host+account+binding combination cannot even attempt
// decryption.
//
// The primary (most-preferred, keys[0] onward) binds to deviceInstanceID -
// a random value generated once and persisted alongside pin.json - rather
// than to a hardware MAC address. An earlier version used the first "up"
// network interface's MAC, which is not a stable identifier: it changes
// order (and therefore value) across a reboot whenever a NIC is added or
// removed, a laptop is docked/undocked, Wi-Fi vs. wired priority flips, or
// a VM's virtual interface gets renumbered - each of those silently and
// permanently locked the user out of an otherwise-correct PIN, since
// Store.Verify surfaces a decrypt failure as a hard error, not a normal
// "wrong PIN" (see UNFINISHED_WORK.md's "Stabilność lokalnego PIN-u
// urządzenia"). A locally-generated, file-persisted random value is not
// observable from the LAN or printed on a device sticker the way a MAC
// address is, so binding to it does not weaken the "stolen pin.json alone
// is worthless" property above - if anything it strengthens it.
//
// Records encrypted before this change still decrypt via the legacy
// MAC-derived keys appended after the primary ones; Store.Verify
// re-encrypts under the primary key the first time a fallback key
// succeeds, so existing installations migrate transparently on next use
// rather than being stranded by this fix.
//
// Multiple identities (uid, then username) are derived for each binding,
// most-preferred first, so a username change alone does not lock out an
// otherwise-unchanged machine/account - callers try each in turn.
func deviceKeys(path string) [][]byte {
	hostname, _ := os.Hostname()
	instanceID, _ := deviceInstanceID(filepath.Dir(path))
	legacyMAC := firstMACAddress()

	var identities []string
	if current, err := user.Current(); err == nil {
		if uid := strings.TrimSpace(current.Uid); uid != "" {
			identities = append(identities, "uid:"+uid)
		}
		if name := strings.TrimSpace(current.Username); name != "" {
			identities = append(identities, "user:"+name)
		}
	}
	if len(identities) == 0 {
		identities = append(identities, "unknown-user")
	}

	keys := make([][]byte, 0, len(identities)*2)
	if instanceID != "" {
		for _, identity := range identities {
			keys = append(keys, deriveKey(hostname, identity, path, instanceID))
		}
	}
	// Legacy fallback for records encrypted before deviceInstanceID existed.
	if legacyMAC != "" {
		for _, identity := range identities {
			keys = append(keys, deriveKey(hostname, identity, path, legacyMAC))
		}
	}
	if len(keys) == 0 {
		// Neither binding was available (e.g. an unwritable root and no
		// network interfaces) - still derive something from what remains,
		// rather than returning no candidate keys at all.
		for _, identity := range identities {
			keys = append(keys, deriveKey(hostname, identity, path, ""))
		}
	}
	return keys
}

func deriveKey(hostname, identity, path, binding string) []byte {
	data := hostname + identity + path + "filees-localpin-v1-salt"
	if binding != "" {
		data += binding
	}
	sum := sha256.Sum256([]byte(data))
	return sum[:]
}

// deviceInstanceID returns a random identifier generated once and persisted
// at <root>/device_id (0600), stable for the lifetime of this installation
// regardless of subsequent network hardware changes. root is the localpin
// store's own directory (already created 0700 by Store.Open before any
// encrypt/decrypt call can reach here).
func deviceInstanceID(root string) (string, error) {
	path := filepath.Join(root, "device_id")
	if raw, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(raw)); id != "" {
			return id, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	id := hex.EncodeToString(buf)
	if err := writeFileAtomic(path, []byte(id), 0o600); err != nil {
		return "", err
	}
	return id, nil
}

// A var, not a plain func, so tests can simulate the exact instability this
// file exists to route around: a NIC appearing, disappearing, or simply
// re-enumerating in a different order across a reboot.
var firstMACAddress = func() string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp != 0 && iface.Flags&net.FlagLoopback == 0 && len(iface.HardwareAddr) > 0 {
			return iface.HardwareAddr.String()
		}
	}
	return ""
}
