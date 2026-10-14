package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlainAddRepairRequiresFeatureAndNeverFallsBack(t *testing.T) {
	wc := t.TempDir()
	c := New(Options{SvnPath: filepath.Join(wc, "must-not-run")}).(*execClient)
	if err := c.UnschedulePlainAdds(t.Context(), wc, []string{"file"}); err == nil || !strings.Contains(err.Error(), "requires native") {
		t.Fatal("CLI fallback admitted", err)
	}
	c = raFake(t, `{"schema":"filees.native-svn/v1","ok":true}`)
	trace := filepath.Join(wc, "trace")
	t.Setenv("FILEES_TEST_RA_TRACE", trace)
	if err := c.UnschedulePlainAdds(t.Context(), wc, []string{"file"}); err == nil || !strings.Contains(err.Error(), "unschedule_plain_add_v1") {
		t.Fatal("old helper admitted", err)
	}
	if raw, err := os.ReadFile(trace); err == nil && len(raw) != 0 || err != nil && !os.IsNotExist(err) {
		t.Fatal("mutation before feature check", string(raw), err)
	}
	if _, err := c.UpdateAdoptPathDepth(t.Context(), wc, "folder", 7, "files"); err == nil {
		t.Fatal("unsupported adoption depth admitted")
	}
	if err := c.CheckAdoptBatch(t.Context()); err == nil || !strings.Contains(err.Error(), "sparse_adopt_batch_v1") {
		t.Fatal("old batch helper admitted", err)
	}
	if _, err := c.UpdateAdoptPathsDepth(t.Context(), wc, []string{"../escape"}, 7, "empty"); err == nil {
		t.Fatal("escape admitted")
	}
}
