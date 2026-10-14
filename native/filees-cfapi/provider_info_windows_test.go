//go:build windows && native_cfapi_probe

package nativecfapiprobe

import (
	"bufio"
	"encoding/json"
	"os/exec"
	"testing"
	"time"
)

func TestProviderInfoAfterAbnormalProviderExit(t *testing.T) {
	// Termination is a documented provider status, not evidence that the
	// persistent registration belongs to another provider or lost its identity.
	root := anchor(t)
	command := exec.Command(helper(t), "connect", "--root", root)
	pipe, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer command.Process.Kill()
	scanner := bufio.NewScanner(pipe)
	received := make(chan answer, 1)
	go func() {
		if scanner.Scan() {
			var a answer
			_ = json.Unmarshal(scanner.Bytes(), &a)
			received <- a
		}
	}()
	select {
	case a := <-received:
		if !a.OK || a.Detail != "connected" {
			t.Fatal(a)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("connect timeout")
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	got := run(t, "", "info", "--root", root)
	t.Logf("after termination: %+v", got)
	if !got.OK || !got.Ours || got.Provider != "FileES" {
		t.Fatalf("terminated provider lost ownership readout: %+v", got)
	}
	if got.Status != 0xc0000001 { // CF_PROVIDER_STATUS_TERMINATED
		t.Fatalf("unexpected status after provider termination: %+v", got)
	}
}
