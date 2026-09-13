package updater

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"filees/internal/serverinstall/manifest"
	"filees/public-shares/storage"
)

// Only the release that declares this layout may migrate operator paths.
const publicStorageLayout = "filees.public-download-storage/v1"

func (r *Runner) planConfigMigrations(m *manifest.Manifest) ([]ConfigMigration, error) {
	server, err := r.planServerConfigMigration(m)
	if err != nil || m == nil {
		return nil, err
	}
	var result []ConfigMigration
	if server != nil {
		result = append(result, *server)
	}
	enabled := false
	for _, contract := range m.Configs {
		for _, change := range contract.DefaultChanged {
			if contract.Name == "public-download-storage" && change.Key == "layout" && change.New == publicStorageLayout {
				enabled = true
			}
		}
	}
	if !enabled {
		return result, nil
	}
	root := r.Config.PublicDownloadsDir
	if root == "" {
		root = "/var/filees-downloads"
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) == string(filepath.Separator) {
		return nil, errors.New("install.public_downloads_dir must be an absolute dedicated directory")
	}
	// The operator selects the volume. Never relocate arbitrary custom paths,
	// copy caches, remove old content or widen a running service's sandbox.
	for _, name := range []string{"server.json", "public-links.json"} {
		path := filepath.Join(r.Config.SysconfDir, name)
		raw, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
			return nil, fmt.Errorf("unsafe public storage configuration: %s", path)
		}
		index := -1
		for i := range result {
			if result[i].Path == path {
				raw, index = result[i].Data, i
			}
		}
		var document map[string]json.RawMessage
		if err := json.Unmarshal(raw, &document); err != nil || document == nil {
			return nil, fmt.Errorf("invalid public storage configuration: %s", path)
		}
		if name == "public-links.json" {
			var schema string
			if json.Unmarshal(document["schema"], &schema) != nil || schema != "filees.public-links/v1" {
				return nil, errors.New("unsupported public links configuration schema")
			}
		}
		section, key, leaf, owner, legacy := "public_shares", "authority_staging_root", "authority", "_filees-state", "filees-public-share-authority"
		if name == "public-links.json" {
			section, key, leaf, owner, legacy = "cache", "root", "cache", "_filees-links", "filees-public-shares-cache"
		}
		var fields map[string]json.RawMessage
		if len(document[section]) == 0 {
			continue
		}
		if err := json.Unmarshal(document[section], &fields); err != nil || fields == nil {
			return nil, fmt.Errorf("invalid %s in %s", section, path)
		}
		var active bool
		if value, exists := fields["enabled"]; exists {
			if err := json.Unmarshal(value, &active); err != nil {
				return nil, fmt.Errorf("invalid %s.enabled", section)
			}
		}
		if !active {
			continue
		}
		var current string
		if value, exists := fields[key]; exists {
			if err := json.Unmarshal(value, &current); err != nil {
				return nil, fmt.Errorf("invalid %s.%s", section, key)
			}
		}
		clean := filepath.Clean(current)
		if current != "" && clean != "/var/tmp/"+legacy && clean != "/tmp/"+legacy {
			continue
		}
		if err := storage.PersistentRoot(root); err != nil {
			return nil, err
		}
		if info, err := os.Stat(filepath.Dir(root)); err != nil || !info.IsDir() {
			return nil, errors.New("public downloads parent must already exist")
		}
		required, budgetKey := int64(2<<30), "max_size"
		if name == "public-links.json" {
			required, budgetKey = 10<<30, "max_size"
		}
		if value, exists := fields[budgetKey]; exists {
			var configured int64
			if err := json.Unmarshal(value, &configured); err != nil || configured < 0 || configured > math.MaxInt64/2 || (name == "public-links.json" && configured == 0) {
				return nil, fmt.Errorf("invalid %s.%s", section, budgetKey)
			}
			if configured > 0 {
				required = configured
				if name == "server.json" {
					required *= 2
				}
			}
		}
		target := filepath.Join(root, leaf)
		fields[key], _ = json.Marshal(target)
		document[section], _ = json.Marshal(fields)
		data, err := json.MarshalIndent(document, "", "  ")
		if err != nil {
			return nil, err
		}
		migration := ConfigMigration{Path: path, FromSchema: "temporary-storage", ToSchema: publicStorageLayout}
		if index >= 0 {
			migration = result[index]
		}
		migration.Data = append(data, '\n')
		migration.Added = append(migration.Added, section+"."+key+"="+target)
		migration.Directories = append(migration.Directories, storageDirectory{Root: root, Path: target, Owner: owner, Required: required})
		if index >= 0 {
			result[index] = migration
		} else {
			result = append(result, migration)
		}
	}
	return result, nil
}

type storageDirectory struct {
	Root, Path, Owner string
	Required          int64
}

// Prepare only dedicated new directories, before configuration is published.
// Existing ownership is never corrected silently and old caches are untouched.
// Empty directories left by an interrupted install are safe to reuse.
func (r *Runner) prepareStorageDirectories(migrations []ConfigMigration) error {
	if err := r.checkStorageCapacity(migrations); err != nil {
		return err
	}
	roots := make(map[string]bool)
	for _, migration := range migrations {
		for _, dir := range migration.Directories {
			roots[dir.Root] = true
		}
	}
	for root := range roots {
		parent, err := filepath.EvalSymlinks(filepath.Dir(root))
		if err != nil {
			return err
		}
		info, err := os.Stat(parent)
		if err != nil {
			return fmt.Errorf("public downloads parent must exist: %w", err)
		}
		rootIdentity, err := r.ownershipManager().Resolve("root", "wheel")
		if err != nil {
			return err
		}
		actual, err := r.ownershipManager().Stat(parent)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode().Perm()&0022 != 0 || actual.UID != rootIdentity.UID {
			return errors.New("public downloads parent must be root-owned and not group/other writable")
		}
	}
	for _, migration := range migrations {
		for _, dir := range migration.Directories {
			for _, spec := range []struct {
				path, owner string
				mode        os.FileMode
			}{
				{dir.Root, "root", 0755}, {dir.Path, dir.Owner, 0700},
			} {
				ownership, err := r.ownershipManager().Resolve(spec.owner, "wheel")
				if err != nil {
					return err
				}
				if err := os.Mkdir(spec.path, spec.mode); err == nil {
					if err := r.ownershipManager().Apply(spec.path, ownership); err != nil {
						return err
					}
				} else if !errors.Is(err, os.ErrExist) {
					return fmt.Errorf("prepare public downloads %s (parent must already exist): %w", spec.path, err)
				}
				info, err := os.Lstat(spec.path)
				if err != nil {
					return err
				}
				actual, err := r.ownershipManager().Stat(spec.path)
				if err != nil {
					return err
				}
				if !info.IsDir() || info.Mode().Perm() != spec.mode || actual != ownership {
					return fmt.Errorf("public download directory %s requires owner=%s group=wheel mode=%04o; refusing to change existing metadata", spec.path, spec.owner, spec.mode)
				}
			}
		}
	}
	return nil
}

// Display the actual volume and the combined budget before approval; recheck
// in prepareStorageDirectories because the plan is not a space reservation.
func (r *Runner) checkStorageCapacity(migrations []ConfigMigration) error {
	var budgets []storage.Budget
	for _, migration := range migrations {
		for _, dir := range migration.Directories {
			budgets = append(budgets, storage.Budget{Path: dir.Path, Required: dir.Required})
		}
	}
	volumes, err := storage.InspectBudgets(budgets)
	if err != nil {
		return err
	}
	for _, volume := range volumes {
		fmt.Fprintf(r.Out, "STORAGE filesystem=%s paths=%s available_bytes=%d required_bytes=%d reserve_bytes=%d\n",
			volume.Device, strings.Join(volume.Paths, ","), volume.Available, volume.Required, volume.Reserve)
	}
	for _, volume := range volumes {
		if err := volume.Check(); err != nil {
			return fmt.Errorf("select a larger install.public_downloads_dir for %s: %w", strings.Join(volume.Paths, ","), err)
		}
	}
	return nil
}
