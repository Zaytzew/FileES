//go:build !nocfapi

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

var requiredAnchorFeatures = []string{
	"sync_root_v1", "shell_sync_root_v1", "placeholders_v1",
	"refuse_delete_rename_v1", "fetch_bridge_v1", "revert_placeholder_v1",
}

type cfapiVersion struct {
	Schema   string   `json:"schema"`
	OK       bool     `json:"ok"`
	Version  string   `json:"version"`
	Features []string `json:"features"`
}

var cfapiReleasePattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

// Compare the product release and protocol, not SVN revision or binary trust.
// No manager is wired on refusal: the existing detach guard preserves data.
// A dev daemon skips only release equality, never schema/features validation.
func anchorHelperCompatible(ctx context.Context, helper, daemonVersion string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, helper, "version")
	cmd.WaitDelay = time.Second
	var out anchorVersionOutput
	cmd.Stdout, cmd.Stderr = &out, io.Discard
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("CFAPI version query failed: %w", err)
	}
	return validateAnchorVersion(out.buffer.Bytes(), daemonVersion)
}

func validateAnchorVersion(raw []byte, daemonVersion string) error {
	var answer cfapiVersion
	if err := json.Unmarshal(raw, &answer); err != nil {
		return errors.New("CFAPI version answer is not one valid JSON object")
	}
	if !answer.OK || answer.Schema != "filees.cfapi/v1" {
		return errors.New("CFAPI version answer has an unsupported schema or failure status")
	}
	if !cfapiReleasePattern.MatchString(answer.Version) {
		return errors.New("CFAPI helper has no valid product version")
	}
	features := make(map[string]bool, len(answer.Features))
	for _, feature := range answer.Features {
		features[feature] = true
	}
	for _, feature := range requiredAnchorFeatures {
		if !features[feature] {
			return fmt.Errorf("CFAPI helper lacks feature %s", feature)
		}
	}
	release, _, _ := strings.Cut(strings.TrimSpace(daemonVersion), "+")
	if release == "dev" {
		return nil
	}
	if !cfapiReleasePattern.MatchString(release) {
		return errors.New("CFAPI daemon has no valid product version")
	}
	if answer.Version != release {
		return fmt.Errorf("CFAPI helper release %s differs from daemon %s", answer.Version, release)
	}
	return nil
}

// Named buffer: embedding bytes.Buffer would expose ReadFrom and bypass Write.
type anchorVersionOutput struct{ buffer bytes.Buffer }

func (w *anchorVersionOutput) Write(p []byte) (int, error) {
	if len(p) > (64<<10)-w.buffer.Len() {
		return 0, errors.New("CFAPI version answer exceeds 64 KiB")
	}
	return w.buffer.Write(p)
}
