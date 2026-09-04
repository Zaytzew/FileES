package updater

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"filees/internal/serverinstall/manifest"
	"filees/pkg/clientview"
	"filees/pkg/serverconfig"
)

const serverConfigV1 = "filees.server-toolchain/v1"
const serverConfigV2FirstSequence uint64 = 855

// ConfigMigration is an installer-owned, transactional rewrite of a mutable
// operator configuration. Runtime binaries remain strict: accepting an older
// generation is the installer's migration job, not a compatibility path in
// every server process.
type ConfigMigration struct {
	Path       string
	FromSchema string
	ToSchema   string
	Added      []string
	Data       []byte
}

// planServerConfigMigration upgrades the configuration expected by the
// binaries in this build. It only knows explicit adjacent generations. That
// makes a missing migration fail before any binary is replaced instead of
// guessing through an unknown configuration shape.
func (r *Runner) planServerConfigMigration(m *manifest.Manifest) (*ConfigMigration, error) {
	if !manifestCarriesServerToolchain(m) {
		return nil, nil
	}
	targetSchema, err := targetServerConfigSchema(m)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(r.Config.SysconfDir, "server.json")
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read server configuration for migration: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("stat server configuration for migration: %w", err)
	}
	if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0o022 != 0) {
		return nil, fmt.Errorf("server configuration %s must be regular and not writable by group or others", path)
	}

	var document map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode server configuration for migration: %w", err)
	}
	if document == nil {
		return nil, errors.New("decode server configuration for migration: root must be an object")
	}
	if err := decoder.Decode(&struct{}{}); err == nil {
		return nil, errors.New("decode server configuration for migration: trailing JSON value")
	} else if !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("decode server configuration for migration: trailing data: %w", err)
	}

	var schema string
	if err := json.Unmarshal(document["schema"], &schema); err != nil {
		return nil, errors.New("server configuration migration requires a string schema")
	}
	if schema == targetSchema {
		if schema == serverconfig.Schema {
			return nil, validateCurrentServerConfigIdentity(document)
		}
		return nil, nil
	}
	switch {
	case schema == serverConfigV1 && targetSchema == serverconfig.Schema:
		// The immutable technical server_id is the only existing value that is
		// both deterministic and already validated as human-readable text. It
		// is a safe initial label; the operator may rename display_name later.
		var invitation struct {
			ServerID string `json:"server_id"`
		}
		if err := json.Unmarshal(document["invitation"], &invitation); err != nil {
			return nil, errors.New("server configuration migration requires invitation.server_id")
		}
		displayName := strings.TrimSpace(invitation.ServerID)
		if err := clientview.ValidateServerDisplayName(displayName); err != nil {
			return nil, fmt.Errorf("derive display_name from invitation.server_id: %w", err)
		}
		if _, exists := document["display_name"]; exists {
			return nil, errors.New("v1 server configuration unexpectedly already contains display_name")
		}
		document["schema"], _ = json.Marshal(serverconfig.Schema)
		document["display_name"], _ = json.Marshal(displayName)
		migrated, err := json.MarshalIndent(document, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("encode migrated server configuration: %w", err)
		}
		return &ConfigMigration{
			Path: path, FromSchema: schema, ToSchema: targetSchema,
			Added: []string{"display_name"}, Data: append(migrated, '\n'),
		}, nil
	case schema == serverconfig.Schema && targetSchema == serverConfigV1:
		delete(document, "display_name")
		document["schema"], _ = json.Marshal(serverConfigV1)
		migrated, err := json.MarshalIndent(document, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("encode migrated server configuration: %w", err)
		}
		return &ConfigMigration{
			Path: path, FromSchema: schema, ToSchema: targetSchema,
			Data: append(migrated, '\n'),
		}, nil
	default:
		return nil, fmt.Errorf("no server configuration migration from schema %q to %q", schema, targetSchema)
	}
}

// targetServerConfigSchema reads the generation declaration from fields that
// schema-v2 installers already understand. This lets a bridge release be
// consumed by the previous installer without extending the signed manifest
// grammar first. The sequence fallback makes historical explicit reverts
// deterministic even though releases before r855 carried no config contract.
func targetServerConfigSchema(m *manifest.Manifest) (string, error) {
	for _, contract := range m.Configs {
		if contract.Name != "server" && filepath.Base(contract.Path) != "server.json" {
			continue
		}
		for _, change := range contract.DefaultChanged {
			if norm(change.Key) != "schema" || strings.TrimSpace(change.New) == "" {
				continue
			}
			target := strings.TrimSpace(change.New)
			if target != serverConfigV1 && target != serverconfig.Schema {
				return "", fmt.Errorf("unsupported target server configuration schema %q", target)
			}
			return target, nil
		}
	}
	if m.Sequence > 0 && m.Sequence < serverConfigV2FirstSequence {
		return serverConfigV1, nil
	}
	return serverconfig.Schema, nil
}

func validateCurrentServerConfigIdentity(document map[string]json.RawMessage) error {
	var displayName string
	if err := json.Unmarshal(document["display_name"], &displayName); err != nil {
		return errors.New("server configuration v2 requires a string display_name")
	}
	if err := clientview.ValidateServerDisplayName(displayName); err != nil {
		return fmt.Errorf("server configuration display_name: %w", err)
	}
	return nil
}

func manifestCarriesServerToolchain(m *manifest.Manifest) bool {
	if m == nil {
		return false
	}
	for _, file := range m.Files {
		base := filepath.Base(strings.ReplaceAll(file.Target, `\`, "/"))
		if base == "filees-public-authority" || base == "filees-operation" || base == "filees-worker" {
			return true
		}
	}
	return false
}
