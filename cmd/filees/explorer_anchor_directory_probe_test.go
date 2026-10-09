//go:build windows && native_cfapi_probe && !nocfapi

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAnchorDirectoryNativeMalformedFrameDisconnects(t *testing.T) {
	for _, frame := range []string{
		"directory-set\tprobe\tfolder\t1\tfolder\t1\t4294967296\n",
		"directory-set\tprobe\tfolder\t1\n",
		"directory-set\tprobe\tfolder\t1\tfolder\t1\t1\nunterminated child",
	} {
		t.Run(fmt.Sprintf("frame-%d", len(frame)), func(t *testing.T) {
			root := t.TempDir()
			p := newLocalStateProbe(t, root, "", "d\t0\tfolder\tfolder\n")
			p.stop()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, p.helper, "connect", "--root", root)
			input, err := command.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			output, err := command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			reader := bufio.NewScanner(output)
			if !reader.Scan() || !strings.Contains(reader.Text(), `"ok":true`) {
				t.Fatal("provider not connected")
			}
			_, _ = io.WriteString(input, frame)
			_ = input.Close()
			for reader.Scan() {
				if strings.HasPrefix(reader.Text(), "directory-set\t") {
					t.Fatal("malformed frame acknowledged")
				}
			}
			if err := command.Wait(); err == nil || ctx.Err() != nil {
				t.Fatal("malformed frame did not disconnect promptly", err, ctx.Err())
			}
			// A fresh provider can connect immediately; the bad reader did not
			// leave a registered connection or partially executing command.
			restart := exec.CommandContext(ctx, p.helper, "connect", "--root", root)
			restart.Stdin = strings.NewReader("")
			if out, err := restart.CombinedOutput(); err != nil || !strings.Contains(string(out), `"ok":true`) {
				t.Fatal("reconnect", string(out), err)
			}
		})
	}
}

// Real Cloud Filter metadata, not a filesystem substitute. No owner WC.
func TestAnchorDirectoryNativeBoundedLargeProof(t *testing.T) {
	const count = anchorDirectoryObjectLimit
	root := t.TempDir()
	var listing, paths strings.Builder
	paths.WriteString(".\n")
	for i := range count {
		rel := fmt.Sprintf("file-%04d.bin", i)
		fmt.Fprintf(&listing, "f\t8192\t%s\t%s\n", rel, rel)
		fmt.Fprintln(&paths, rel)
	}
	// Seed before connecting, as for the initial tree in the real provider.
	p := newLocalStateProbe(t, root, "", listing.String())
	m := &anchorManager{helper: p.helper}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	started := time.Now()
	a, err := m.call(ctx, paths.String(), "tree-states", "--root", root)
	cancel()
	if err != nil || !a.OK || len(a.States) != count+1 {
		t.Fatal("bounded large observation", len(a.States), err)
	}
	t.Log("attribute observation", count, time.Since(started))
	var payload strings.Builder
	for i := range count {
		s := a.States[i+1]
		if !s.OK || !s.Placeholder || !s.Partial || !s.InSync || s.Modified != 0 {
			t.Fatal("unproven remote child", i, s)
		}
		fmt.Fprintf(&payload, "file-%04d.bin\t%s\t%s\n", i, s.USN, s.Identity)
	}
	parent := a.States[0]
	started = time.Now()
	p.write(fmt.Sprintf("directory-set\tlarge\t.\t%s\t%s\t1\t%d\n%s", parent.USN, parent.Identity, count, payload.String()))
	select {
	case reply := <-p.reply:
		if reply[2] != hresultOK {
			t.Fatal("large proof", reply)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("large proof exceeded writer budget")
	}
	t.Log("connected parent proof", count, time.Since(started))
	// A sync root need not expose placeholder InSync; the connected ACK
	// still verifies its entire tree without converting or pinning it.
	select {
	case request := <-p.fetch:
		t.Fatal("large proof hydrated content", request)
	default:
	}
	// The one extra row is refused before any filesystem read/mutation.
	if a, err := m.call(t.Context(), paths.String()+".\n", "tree-states", "--root", root); err == nil && a.OK {
		t.Fatal("unbounded metadata request admitted")
	}
}

func TestAnchorDirectoryNativeCAS(t *testing.T) {
	root := t.TempDir()
	p := newLocalStateProbe(t, root, "", "d\t0\tfolder\tfolder\nf\t8192\tremote.bin\tremote.bin\n")
	m := &anchorManager{helper: p.helper}
	read := func(rel string) cfapiAnswer {
		t.Helper()
		a, e := m.call(t.Context(), rel+"\n", "tree-states", "--root", root)
		if e != nil || !a.OK || len(a.States) != 1 || !a.States[0].OK {
			t.Fatalf("state %s: %+v %v", rel, a, e)
		}
		return a.States[0]
	}
	set := func(rel string, state cfapiAnswer, clean bool, input string) error {
		t.Helper()
		flag := "0"
		if clean {
			flag = "1"
		}
		p.write(fmt.Sprintf("directory-set\tprobe\t%s\t%s\t%s\t%s\t%d\n%s", rel, state.USN, state.Identity, flag, strings.Count(input, "\n"), input))
		select {
		case answer := <-p.reply:
			if answer[2] != hresultOK {
				return errors.New(answer[2])
			}
			return nil
		case <-time.After(10 * time.Second):
			t.Fatal("directory acknowledgement timed out")
			return nil
		}
	}
	folder := read("folder")
	if err := set("folder", folder, false, ""); err != nil {
		t.Fatal("clear", err)
	}
	folder = read("folder")
	if folder.InSync {
		t.Fatal("directory not cleared")
	}
	if err := set("folder", folder, true, ""); err != nil {
		t.Fatal("empty directory", err)
	}
	if !read("folder").InSync {
		t.Fatalf("empty directory not confirmed: %+v", read("folder"))
	}
	// Adding an ordinary file cannot be hidden by confirmation of its parent.
	if err := os.WriteFile(filepath.Join(root, "folder", "local.txt"), []byte("not published"), 0600); err != nil {
		t.Fatal(err)
	}
	folder = read("folder")
	if err := set("folder", folder, true, ""); err == nil {
		t.Fatal("unknown extra child accepted")
	}
	if read("folder").InSync {
		t.Fatal("unknown child left the old parent marker clean")
	}
	folder = read("folder")
	if err := set("folder", folder, false, ""); err != nil {
		t.Fatal(err)
	}
	p.accept("folder/local.txt", false)
	local := read("folder/local.txt")
	folder = read("folder")
	input := fmt.Sprintf("local.txt\t%s\t%s\n", local.USN, local.Identity)
	if err := set("folder", folder, true, input); err == nil {
		t.Fatal("unpublished child accepted")
	}
	if read("folder/local.txt").InSync {
		t.Fatal("child was falsely marked clean")
	}
	p.accept("folder/local.txt", true)
	local = read("folder/local.txt")
	folder = read("folder")
	input = fmt.Sprintf("local.txt\t%s\t%s\n", local.USN, local.Identity)
	if err := set("folder", folder, true, input); err != nil {
		t.Fatal("clean local directory", err)
	}
	if !read("folder").InSync {
		t.Fatal("parent remained pending")
	}
	// A newer child cannot be acknowledged with an earlier observation.
	if err := os.WriteFile(filepath.Join(root, "folder", "local.txt"), []byte("later edit"), 0600); err != nil {
		t.Fatal(err)
	}
	folder = read("folder")
	if err := set("folder", folder, true, input); err == nil {
		t.Fatal("stale child accepted")
	}
	folder = read("folder")
	if err := set("folder", folder, false, ""); err != nil {
		t.Fatal(err)
	}
	old := folder
	if err := os.Mkdir(filepath.Join(root, "folder", "new"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := set("folder", old, false, ""); err == nil {
		t.Fatal("stale directory USN accepted")
	}
	// Root can be confirmed without converting it or pinning any future child.
	rootState := read(".")
	remote := read("remote.bin")
	folder = read("folder")
	input = fmt.Sprintf("folder\t%s\t%s\nremote.bin\t%s\t%s\n", folder.USN, folder.Identity, remote.USN, remote.Identity)
	if err := set(".", rootState, true, input); err == nil {
		t.Fatal("pending child directory accepted")
	}
	// Known byte-free remote children are valid evidence, not a hydration request.
	if err := set(".", read("."), false, ""); err != nil {
		t.Fatal("root pending", err)
	}
	select {
	case request := <-p.fetch:
		t.Fatal("metadata confirmation fetched", request)
	default:
	}
	if !read("remote.bin").Partial {
		t.Fatal("remote content was materialized")
	}
	if got, err := os.ReadFile(filepath.Join(root, "folder", "local.txt")); err != nil || string(got) != "later edit" {
		t.Fatal("local bytes changed", err)
	}
	// Child names are canonical names, never paths/private data selections.
	for _, bad := range []string{"../remote.bin", ".svn", ".filees", "local.txt/local.txt"} {
		if err := set("folder", read("folder"), true, strings.Replace(input, "folder\t", bad+"\t", 1)); err == nil {
			t.Fatal("invalid child name admitted", bad)
		}
	}
}

func TestAnchorDirectoryNativeRemoteAndNested(t *testing.T) {
	root := t.TempDir()
	p := newLocalStateProbe(t, root, "", "d\t0\tfolder\tfolder\nf\t8192\tremote.bin\tremote.bin\n")
	m := &anchorManager{helper: p.helper}
	// Connected command creates one nested remote placeholder, no data.
	p.write("tree-set\tprobe\tfolder/leaf.bin\tf\t8192\t\tfolder/leaf.bin\t0")
	select {
	case reply := <-p.reply:
		if reply[2] != hresultOK {
			t.Fatal(reply)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("seed timeout")
	}
	read := func(rel string) cfapiAnswer {
		a, e := m.call(t.Context(), rel+"\n", "tree-states", "--root", root)
		if e != nil || len(a.States) != 1 || !a.States[0].OK {
			t.Fatal(a, e)
		}
		return a.States[0]
	}
	set := func(dir string, desc []string) {
		t.Helper()
		parent := read(dir)
		var input []string
		for _, rel := range desc {
			s := read(rel)
			name := rel
			if dir != "." {
				name = strings.TrimPrefix(name, dir+"/")
			}
			input = append(input, fmt.Sprintf("%s\t%s\t%s", name, s.USN, s.Identity))
		}
		packet := fmt.Sprintf("directory-set\tprobe\t%s\t%s\t%s\t1\t%d", dir, parent.USN, parent.Identity, len(input))
		if len(input) > 0 {
			packet += "\n" + strings.Join(input, "\n")
		}
		p.write(packet)
		select {
		case reply := <-p.reply:
			if reply[2] != hresultOK {
				t.Fatal("confirm", dir, reply)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("ACK timeout")
		}
	}
	set("folder", []string{"folder/leaf.bin"})
	set(".", []string{"folder", "folder/leaf.bin", "remote.bin"})
	if !read(".").InSync {
		t.Log("sync root does not expose placeholder InSync; ACK is separate from hydration badge")
	}
	if !read("folder").InSync {
		t.Fatal("nested directory remained pending")
	}
	for _, file := range []string{"folder/leaf.bin", "remote.bin"} {
		if !read(file).Partial {
			t.Fatal("hydration", file)
		}
	}
	select {
	case request := <-p.fetch:
		t.Fatal("unexpected fetch", request)
	default:
	}
}
