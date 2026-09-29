package commit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"filees/pkg/client"
	"filees/pkg/talk"
)

type metadataReconcileClient struct {
	*revisionClient
	details []client.ConflictDetail
	err     error
}

func (c *metadataReconcileClient) ConflictDetails(context.Context, string, string) ([]client.ConflictDetail, error) {
	return c.details, c.err
}

func TestReconcileUsesMetadataNotMineSuffix(t *testing.T) {
	for _, mode := range []string{"numbered-mine", "binary-without-mine", "missing-metadata", "unreadable-metadata", "missing-artifact"} {
		t.Run(mode, func(t *testing.T) {
			wc := t.TempDir()
			for path, text := range map[string]string{"file": "live", "file.mine": "ordinary", "file.2.mine": "local"} {
				if err := os.WriteFile(filepath.Join(wc, path), []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
			}
			cli := &metadataReconcileClient{revisionClient: &revisionClient{theirs: "server"}, details: []client.ConflictDetail{{Type: "text", Mine: "file.2.mine"}}}
			want := "local"
			switch mode {
			case "binary-without-mine":
				cli.details[0].Mine = ""
				want = "live"
			case "missing-metadata":
				cli.details = nil
			case "unreadable-metadata":
				cli.err = errors.New("unavailable")
			case "missing-artifact":
				cli.details[0].Mine = "file.3.mine"
			}
			s := &Service{Cli: cli, Logger: talk.With("metadata-test")}
			s.ReconcileUpdateConflicts(t.Context(), wc, "C file\n")
			if mode != "numbered-mine" && mode != "binary-without-mine" {
				if len(cli.resolved) != 0 {
					t.Fatal("resolved without evidence")
				}
				return
			}
			if len(cli.resolved) != 1 {
				t.Fatal("not resolved")
			}
			paths, _ := filepath.Glob(filepath.Join(wc, kolizjeDir, "*_lokalne", "file"))
			if len(paths) != 1 {
				t.Fatal("missing backup", paths)
			}
			got, err := os.ReadFile(paths[0])
			if err != nil || string(got) != want {
				t.Fatalf("backup %q %v", got, err)
			}
			got, err = os.ReadFile(filepath.Join(wc, "file.mine"))
			if err != nil || string(got) != "ordinary" {
				t.Fatalf("ordinary file changed %q %v", got, err)
			}
		})
	}
}
