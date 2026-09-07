//go:build openbsd

package storage

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestStorageUnveilChild(t *testing.T) {
	root := os.Getenv("FILEES_STORAGE_PROBE")
	if root == "" {
		return
	}
	if err := unix.Unveil(root, "rwc"); err != nil {
		os.Exit(2)
	}
	if err := unix.UnveilBlock(); err != nil {
		os.Exit(3)
	}
	if err := unix.PledgePromises("stdio rpath wpath cpath"); err != nil {
		os.Exit(4)
	}
	fmt.Println("ready")
	var signal [1]byte
	if _, err := os.Stdin.Read(signal[:]); err != nil {
		os.Exit(5)
	}
	err := os.MkdirAll(root, 0700)
	if os.Getenv("FILEES_STORAGE_REMOVED") == "1" {
		if !errors.Is(err, os.ErrNotExist) {
			fmt.Printf("unexpected: %v\n", err)
			os.Exit(6)
		}
	} else {
		if err != nil {
			os.Exit(7)
		}
		if err := RequireSpace(root, 1); err != nil {
			fmt.Println(err)
			os.Exit(9)
		}
		if err := os.WriteFile(filepath.Join(root, "leaf"), []byte("bytes"), 0600); err != nil {
			fmt.Println(err)
			os.Exit(8)
		}
	}
	os.Exit(0)
}
func TestUnveilDeletedScratchRootAndPersistentSibling(t *testing.T) {
	for _, removed := range []bool{true, false} {
		root := t.TempDir()
		scratch, persistent := filepath.Join(root, "scratch"), filepath.Join(root, "persistent")
		for _, dir := range []string{scratch, persistent} {
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
		}
		target := persistent
		if removed {
			target = scratch
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStorageUnveilChild$")
		cmd.Env = append(os.Environ(), "FILEES_STORAGE_PROBE="+target)
		if removed {
			cmd.Env = append(cmd.Env, "FILEES_STORAGE_REMOVED=1")
		}
		in, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
		scanner := bufio.NewScanner(out)
		if !scanner.Scan() || scanner.Text() != "ready" {
			t.Fatal("child did not establish unveil")
		}
		// Remove only the empty scratch fixture, never real service directories.
		if err := os.Remove(scratch); err != nil {
			t.Fatal(err)
		}
		if _, err := in.Write([]byte{1}); err != nil {
			t.Fatal(err)
		}
		in.Close()
		for scanner.Scan() {
			t.Log(scanner.Text())
		}
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
}
