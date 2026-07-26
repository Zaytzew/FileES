//go:build linux

package platform

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const stateOwnerDefault = "_filees-state"

type linuxBackend struct {
	useradd   string
	userdel   string
	sshd      string
	systemctl string
	openssl   string
	sshKeygen string
}

func New() (Backend, error) {
	b := &linuxBackend{}
	var err error
	for dst, src := range map[*string]string{
		&b.useradd:   "useradd",
		&b.userdel:   "userdel",
		&b.sshd:      "sshd",
		&b.systemctl: "systemctl",
		&b.openssl:   "openssl",
		&b.sshKeygen: "ssh-keygen",
	} {
		if *dst, err = resolve(src); err != nil {
			return nil, err
		}
	}
	return b, nil
}

func (b *linuxBackend) EnsureUsers(names []string) ([]string, error) {
	var created []string
	for _, name := range names {
		if userExists(name) {
			continue
		}
		args := []string{"--system", "--no-create-home", "--shell", "/usr/sbin/nologin",
			"--comment", "FileES " + name, name}
		if out, err := exec.Command(b.useradd, args...).CombinedOutput(); err != nil {
			return created, fmt.Errorf("useradd %s: %w: %s", name, err, strings.TrimSpace(string(out)))
		}
		created = append(created, name)
	}
	return created, nil
}

// RegisterLoginConf is a no-op on Linux (no login.conf capability database).
func (b *linuxBackend) RegisterLoginConf(path string) error { return nil }

func (b *linuxBackend) EnsureSSHDInclude(sshdConfDir, sshdConfig string) (string, error) {
	include := "Include " + sshdConfDir + "/*.conf"
	return ensureSSHDInclude(sshdConfig, include)
}

func ensureSSHDInclude(sshdConfig, include string) (string, error) {
	data, err := os.ReadFile(sshdConfig)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", sshdConfig, err)
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == include {
			return "", nil
		}
	}

	backup := sshdConfig + ".filees-before-install"
	if err := copyFileMode(sshdConfig, backup, 0); err != nil {
		return "", fmt.Errorf("backup %s: %w", sshdConfig, err)
	}
	f, err := os.OpenFile(sshdConfig, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return "", err
	}
	_, writeErr := fmt.Fprintf(f, "\n%s\n", include)
	closeErr := f.Close()
	if writeErr != nil {
		return "", writeErr
	}
	return backup, closeErr
}

func (b *linuxBackend) ReloadSSHD() error {
	if out, err := exec.Command(b.sshd, "-t").CombinedOutput(); err != nil {
		return fmt.Errorf("sshd -t: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command(b.systemctl, "reload", "ssh").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl reload ssh: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (b *linuxBackend) ApplyStateDirs(stateOwner string) error {
	uid, gid, err := lookupUID(stateOwner)
	if err != nil {
		return fmt.Errorf("lookup %s: %w", stateOwner, err)
	}

	dirs := []struct {
		path string
		mode os.FileMode
		uid  int
		gid  int
	}{
		{"/var/filees", 0o700, uid, gid},
		{"/var/filees/onboarding", 0o700, uid, gid},
		{"/var/filees/onboarding/tickets", 0o700, uid, gid},
		{"/var/filees/onboarding/operations", 0o700, uid, gid},
		{"/var/filees/onboarding/audit", 0o700, uid, gid},
		{"/var/filees/activation", 0o700, uid, gid},
		{"/var/filees/activation/records", 0o700, uid, gid},
		{"/var/filees/activation/proofs", 0o700, uid, gid},
		{"/var/filees/sessions", 0o700, uid, gid},
		{"/etc/filees", 0o700, uid, gid},
	}
	for _, d := range dirs {
		if err := mkdirChown(d.path, d.mode, d.uid, d.gid); err != nil {
			return fmt.Errorf("setup %s: %w", d.path, err)
		}
	}

	lock := "/var/filees/onboarding/.toolchain.lock"
	if _, err := os.Stat(lock); os.IsNotExist(err) {
		f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return fmt.Errorf("create %s: %w", lock, err)
		}
		f.Close()
		if err := os.Lchown(lock, uid, gid); err != nil {
			return err
		}
	}
	return nil
}

func (b *linuxBackend) GenerateSecrets(confDir string) error {
	if err := os.MkdirAll(confDir, 0o700); err != nil {
		return err
	}
	uid, gid, err := lookupUID(stateOwnerDefault)
	if err != nil {
		return fmt.Errorf("lookup %s: %w", stateOwnerDefault, err)
	}

	pepper := filepath.Join(confDir, "otp.pepper")
	if _, err := os.Stat(pepper); os.IsNotExist(err) {
		out, err := exec.Command(b.openssl, "rand", "-base64", "32").Output()
		if err != nil {
			return fmt.Errorf("generate otp.pepper: %w", err)
		}
		if err := os.WriteFile(pepper, out, 0o600); err != nil {
			return err
		}
		if err := os.Lchown(pepper, uid, gid); err != nil {
			return err
		}
	}

	key := filepath.Join(confDir, "worker_ed25519")
	if _, err := os.Stat(key); os.IsNotExist(err) {
		if out, err := exec.Command(b.sshKeygen, "-q", "-t", "ed25519",
			"-N", "", "-C", "filees-worker-v1", "-f", key).CombinedOutput(); err != nil {
			return fmt.Errorf("generate worker key: %w: %s", err, strings.TrimSpace(string(out)))
		}
		for _, p := range []struct {
			path string
			mode os.FileMode
		}{
			{key, 0o600},
			{key + ".pub", 0o644},
		} {
			if err := os.Chmod(p.path, p.mode); err != nil {
				return err
			}
			if err := os.Lchown(p.path, uid, gid); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *linuxBackend) RemoveUser(name string) error {
	if !userExists(name) {
		return nil
	}
	if out, err := exec.Command(b.userdel, "--remove", name).CombinedOutput(); err != nil {
		return fmt.Errorf("userdel %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (b *linuxBackend) RemoveLoginConf(path string) error { return nil }

func copyFileMode(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if mode == 0 {
		st, err := in.Stat()
		if err != nil {
			return err
		}
		// Keep setuid/setgid/sticky, not just Perm()'s 0777 slice.
		mode = st.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Chmod(dst, mode)
}
