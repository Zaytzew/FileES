package localpin

import (
	"crypto/sha256"
	"net"
	"os"
	"os/user"
	"strings"
)

// deviceKeys derives one or more AES-256 keys tied to this machine and
// account, mirroring the technique already used and proven in the
// (separate, unrelated) syschat project's password-at-rest encryption
// (syschat/internal/config.go deriveEncryptionKeys/deriveEncryptionKeyMaterial):
// sha256(hostname + identity + path + a fixed salt + MAC address). None of
// hostname/uid/username/MAC is ever persisted anywhere - the key exists only
// as a runtime derivation, so a stolen pin.json is worthless off-device: a
// PIN's entropy (4-6 digits) is far too low for a password hash like bcrypt
// to meaningfully protect against offline brute force of a stolen hash,
// but an attacker without this exact host+account+MAC combination cannot
// even attempt decryption. path is the pin.json path itself, tying the key
// to this specific installation the same way syschat ties it to its own
// config file path. Multiple identities (uid, then username) are derived,
// most-preferred first, so a username change alone does not lock out an
// otherwise-unchanged machine/account - callers try each in turn.
func deviceKeys(path string) [][]byte {
	hostname, _ := os.Hostname()
	mac := firstMACAddress()

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

	keys := make([][]byte, 0, len(identities))
	for _, identity := range identities {
		keys = append(keys, deriveKey(hostname, identity, path, mac))
	}
	return keys
}

func deriveKey(hostname, identity, path, mac string) []byte {
	data := hostname + identity + path + "filees-localpin-v1-salt"
	if mac != "" {
		data += mac
	}
	sum := sha256.Sum256([]byte(data))
	return sum[:]
}

func firstMACAddress() string {
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
