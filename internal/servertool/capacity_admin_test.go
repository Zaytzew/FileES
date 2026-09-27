//go:build !windows

package servertool

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"filees/internal/storagewatch"
	"filees/pkg/serverconfig"
	"github.com/google/uuid"
)

func TestCapacityCronSMTPAndSandbox(t *testing.T) {
	mucc, e := exec.LookPath("svnmucc")
	if e != nil {
		t.Skip("svnmucc unavailable")
	}
	path, _, _ := writeRepoPruneFixtureConfig(t)
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var conf map[string]any
	if e = json.Unmarshal(raw, &conf); e != nil {
		t.Fatal(e)
	}
	tools := sandboxTempDir(t)
	repo := conf["repositories"].(map[string]any)
	if e = os.Symlink(repo["svnadmin_binary"].(string), filepath.Join(tools, "svnadmin")); e != nil {
		t.Fatal(e)
	}
	muccLink := filepath.Join(tools, "svnmucc")
	if e = os.Symlink(mucc, muccLink); e != nil {
		t.Fatal(e)
	}
	repo["svnadmin_binary"] = filepath.Join(tools, "svnadmin")
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	var messages atomic.Int32
	var rejectMail atomic.Bool
	go func() {
		for {
			c, e := listener.Accept()
			if e != nil {
				return
			}
			go func() {
				defer c.Close()
				if rejectMail.Load() {
					fmt.Fprint(c, "421 temporarily unavailable\r\n")
					return
				}
				r := bufio.NewReader(c)
				fmt.Fprint(c, "220 test relay\r\n")
				data := false
				for {
					line, e := r.ReadString('\n')
					if e != nil {
						return
					}
					if data {
						if line == ".\r\n" {
							messages.Add(1)
							data = false
							fmt.Fprint(c, "250 queued\r\n")
						}
						continue
					}
					switch {
					case strings.HasPrefix(line, "DATA"):
						data = true
						fmt.Fprint(c, "354 send data\r\n")
					case strings.HasPrefix(line, "QUIT"):
						fmt.Fprint(c, "221 bye\r\n")
						return
					default:
						fmt.Fprint(c, "250 OK\r\n")
					}
				}
			}()
		}
	}()
	conf["smtp"].(map[string]any)["address"] = listener.Addr().String()
	raw, _ = json.Marshal(conf)
	if e = os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	cfg, e := serverconfig.LoadFor(path, serverconfig.SecretActivation)
	if e != nil {
		t.Fatal(e)
	}
	realm := uuid.NewString()
	rpath := filepath.Join(cfg.Activation.ServiceWorkingCopy, "admin", "realms", realm+".json")
	if e = os.MkdirAll(filepath.Dir(rpath), 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(rpath, []byte("{}"), 0600); e != nil {
		t.Fatal(e)
	}
	setting := capacityConfig{Schema: "filees.capacity-alerts/v1", Realm: realm, Email: "admin@example.test", StateDir: filepath.Join(sandboxTempDir(t), "state"), Policy: storagewatch.DefaultPolicy()}
	setting.Policy.WarningBytes = 1 << 61
	setting.Policy.CriticalBytes = 1 << 60
	spath := filepath.Join(filepath.Dir(path), "capacity-alerts.json")
	save := func() {
		t.Helper()
		b, _ := json.Marshal(setting)
		if e = os.WriteFile(spath, b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	save()
	call := func(want int) {
		t.Helper()
		var out, stderr bytes.Buffer
		code := runAdminInvocation(t, []string{"-config", path, "alert", "capacity"}, &out, &stderr)
		if code != want || out.Len() != 0 {
			t.Fatalf("exit=%d want=%d out=%s stderr=%s", code, want, out.String(), stderr.String())
		}
	}
	call(ExitOK)
	first := messages.Load()
	if first < 1 {
		t.Fatal("no fallback email")
	}
	call(ExitOK)
	if messages.Load() != first {
		t.Fatal("repeated state sent email again")
	}
	// Recovery is silent, even if the replica cannot currently be updated.
	setting.Policy = storagewatch.Policy{WarningPercent: 0.00001, CriticalPercent: 0.000001, HysteresisPercent: 0.000001, WarningBytes: 2, CriticalBytes: 1, HysteresisBytes: 1}
	save()
	call(ExitOK)
	if messages.Load() != first {
		t.Fatal("recovery sent email")
	}
	// A failed SVN commit must not prevent an independently queued mail.
	if e = os.Remove(muccLink); e != nil {
		t.Fatal(e)
	}
	falseBin, e := exec.LookPath("false")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(falseBin, muccLink); e != nil {
		t.Fatal(e)
	}
	setting.Policy = storagewatch.DefaultPolicy()
	setting.Policy.WarningBytes = 1 << 61
	setting.Policy.CriticalBytes = 1 << 60
	save()
	call(ExitTempFail)
	if messages.Load() <= first {
		t.Fatal("SVN failure swallowed mail")
	}
	after := messages.Load()
	call(ExitTempFail)
	if messages.Load() != after {
		t.Fatal("SVN retry duplicated accepted email")
	}
	// SMTP failure retains the durable intent while the GUI path still works.
	if e = os.Remove(muccLink); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(mucc, muccLink); e != nil {
		t.Fatal(e)
	}
	setting.Policy = storagewatch.Policy{WarningPercent: 0.00001, CriticalPercent: 0.000001, HysteresisPercent: 0.000001, WarningBytes: 2, CriticalBytes: 1, HysteresisBytes: 1}
	save()
	call(ExitOK)
	rejectMail.Store(true)
	setting.Policy = storagewatch.DefaultPolicy()
	setting.Policy.WarningBytes = 1 << 61
	setting.Policy.CriticalBytes = 1 << 60
	save()
	call(ExitTempFail)
	stored, e := storagewatch.Load(filepath.Join(setting.StateDir, "capacity.json"), realm, setting.Email)
	if e != nil || len(stored.Pending) == 0 {
		t.Fatal("SMTP failure lost retry", e, stored)
	}
	if messages.Load() != after {
		t.Fatal("rejected SMTP counted as delivered")
	}
	rejectMail.Store(false)
	call(ExitOK)
	if messages.Load() <= after {
		t.Fatal("mail retry not delivered")
	}
	after = messages.Load()
	call(ExitOK)
	if messages.Load() != after {
		t.Fatal("receipt not durable")
	}

}
