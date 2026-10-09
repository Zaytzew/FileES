package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"filees/internal/nativeruntime"
)

// The modified library's two shipped source notices must be current too:
// checking only the C helper inventory would miss changes to library sources.
// Ordinary Linux/Store runtimes without either notice remain supported.
func verifyConditionalSources(source, stage string, required bool) error {
	pairs := [][2]string{{"filees_release.inc", "FileES-SVN-modification.inc"}, {"apply-runtime.cjs", "FileES-SVN-transformation.cjs"}}
	found := 0
	for _, pair := range pairs {
		packed, err := os.ReadFile(filepath.Join(stage, "notices", pair[1]))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		current, err := os.ReadFile(filepath.Join(source, "native", "subversion-filees", pair[0]))
		if err != nil {
			return err
		}
		if !bytes.Equal(packed, current) {
			return fmt.Errorf("conditional SVN runtime source changed: %s; rebuild the runtime and stage", pair[0])
		}
		found++
	}
	if found == 1 || (required && found != 2) {
		return errors.New("conditional SVN runtime requires both current modification sources")
	}
	return nil
}

func verifyAnchorReleaseReceipt(raw []byte, schema, version string, features ...string) error {
	var got struct {
		OK       bool     `json:"ok"`
		Schema   string   `json:"schema"`
		Version  string   `json:"version"`
		Features []string `json:"features"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		return err
	}
	if !got.OK || got.Schema != schema || (version != "" && got.Version != version) {
		return errors.New("incompatible anchor release helper receipt")
	}
	for _, need := range features {
		found := false
		for _, feature := range got.Features {
			found = found || feature == need
		}
		if !found {
			return fmt.Errorf("anchor release helper lacks %s", need)
		}
	}
	return nil
}

// Build-time only, before the output directory exists. Validate the complete
// measured closure before loading it; do not borrow a DLL from PATH or cwd.
// Feature receipts are compatibility checks, not a new signature/trust root.
func verifyAnchorReleaseSet(source, stage, cfapi string) error {
	if runtime.GOOS != "windows" {
		return errors.New("Windows anchor release packaging requires a Windows build host")
	}
	if err := verifySources(source, stage); err != nil {
		return err
	}
	if err := verifyConditionalSources(source, stage, true); err != nil {
		return err
	}
	if _, err := nativeruntime.Pack(stage); err != nil {
		return err
	}
	stage, err := filepath.Abs(stage)
	if err != nil {
		return err
	}
	cfapi, err = filepath.Abs(cfapi)
	if err != nil {
		return err
	}
	base, err := os.ReadFile(filepath.Join(source, "VERSION"))
	if err != nil {
		return err
	}
	inspect := func(exe, verb string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, exe, verb)
		command.Dir = stage
		command.Env = []string{"SystemRoot=" + os.Getenv("SystemRoot"), "PATH=" + filepath.Join(os.Getenv("SystemRoot"), "System32")}
		command.WaitDelay = time.Second
		return command.Output()
	}
	raw, err := inspect(filepath.Join(stage, "filees-svn.exe"), "--version")
	if err != nil {
		return fmt.Errorf("load staged SVN runtime: %w", err)
	}
	if err := verifyAnchorReleaseReceipt(raw, "filees.native-svn/v1", "", "sparse_exclude_bound_v1", "sparse_exclude_idle_bound_v1", "sparse_resume_bound_v1", "sparse_inspect_excluded_idle_v1", "info_inspect_remote_v1"); err != nil {
		return err
	}
	raw, err = inspect(cfapi, "version")
	if err != nil {
		return fmt.Errorf("load CFAPI helper: %w", err)
	}
	return verifyAnchorReleaseReceipt(raw, "filees.cfapi/v1", strings.TrimSpace(string(base)), "prepare_release_bound_v1", "restore_release_v1", "release_observation_v1", "remote_tree_v1")
}
