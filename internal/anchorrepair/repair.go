// Package anchorrepair removes virtual-folder entries whose cloud metadata
// Windows can no longer read.
//
// Such an entry can be neither opened, repaired nor deleted while the cloud
// files filter is attached to its volume
// (implementation notes (not distributed)). The only way
// found is to detach the filter from the volume, delete the entries and attach
// it again, which needs administrator rights and briefly affects every cloud
// provider on that volume. Repair therefore runs as a short-lived elevated
// process the user agreed to, and trusts nothing it was handed: it is given a
// folder, decides by itself what in it is damaged, deletes only that, and
// attaches the filter again on every way out.
package anchorrepair

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

const (
	Schema = "filees.anchor-repair/v1"
	// Provider is the sync root provider name the Explorer anchor helper
	// registers (FILEES_CFAPI_PROVIDER in native/filees-cfapi).
	Provider = "FileES"

	maxScanned = 500000
	maxDamaged = 20000
)

// Result is what the elevated process leaves for the GUI. Removed and
// Remaining are relative to Root.
type Result struct {
	Schema     string   `json:"schema"`
	Run        string   `json:"run,omitempty"`
	Root       string   `json:"root"`
	Volume     string   `json:"volume,omitempty"`
	Found      int      `json:"found"`
	Removed    []string `json:"removed,omitempty"`
	Remaining  []string `json:"remaining,omitempty"`
	Detached   bool     `json:"detached"`
	Reattached bool     `json:"reattached"`
	Error      string   `json:"error,omitempty"`
	Detail     string   `json:"detail,omitempty"`
}

// Complete says the folder is usable again: nothing damaged is left and the
// filter is back.
func (result Result) Complete() bool {
	return result.Error == "" && len(result.Remaining) == 0 && (!result.Detached || result.Reattached)
}

// Damaged is one unreadable entry, relative to the folder.
type Damaged struct {
	Path      string
	Directory bool
}

// Entry is one name in a directory, read without opening it.
type Entry struct {
	Name      string
	Directory bool
	// Reparse is set for any reparse point; Cloud only for a cloud files one.
	Reparse, Cloud bool
}

// System is everything Repair asks of Windows.
type System interface {
	// SyncRootProvider names the provider that registered root as a sync root.
	SyncRootProvider(root string) (string, error)
	List(directory string) ([]Entry, error)
	// Unreadable opens the entry itself and reports whether the cloud files
	// filter refused its metadata.
	Unreadable(path string) (bool, error)
	Detach(volume string) error
	Attach(volume string) error
	// RemoveCloudEntry deletes one entry that is a cloud files reparse point
	// located under root, without following anything.
	RemoveCloudEntry(root, path string, directory bool) error
}

var (
	ErrNotAnchor       = errors.New("not a FileES virtual folder")
	ErrRefusedPath     = errors.New("refused path")
	ErrTooLarge        = errors.New("folder is too large to inspect")
	ErrElevationNeeded = errors.New("administrator rights are required")
)

// volumeOf accepts only a folder on a drive-letter volume that is not the
// volume itself: no UNC, no device paths, nothing relative.
func volumeOf(root string) (string, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", ErrRefusedPath
	}
	volume := filepath.VolumeName(root)
	if len(volume) != 2 || volume[1] != ':' || !(volume[0] >= 'A' && volume[0] <= 'Z' || volume[0] >= 'a' && volume[0] <= 'z') {
		return "", ErrRefusedPath
	}
	if rest := strings.Trim(root[len(volume):], `\/`); rest == "" {
		return "", ErrRefusedPath
	}
	return strings.ToUpper(volume), nil
}

// Scan lists, relative to root, the entries whose metadata is unreadable. It
// descends only through ordinary directories and readable cloud directories;
// a link or junction is never followed, and FileES and Subversion metadata is
// never inspected.
func Scan(system System, root string) ([]Damaged, error) {
	var damaged []Damaged
	scanned := 0
	pending := []string{""}
	for len(pending) > 0 {
		relative := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		entries, err := system.List(filepath.Join(root, relative))
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", relative, err)
		}
		for _, entry := range entries {
			if relative == "" && (strings.EqualFold(entry.Name, ".svn") || strings.EqualFold(entry.Name, ".filees")) {
				continue
			}
			if scanned++; scanned > maxScanned {
				return nil, ErrTooLarge
			}
			child := filepath.Join(relative, entry.Name)
			// Every entry is opened: the filter hides the cloud reparse tag from
			// an ordinary process's directory listing, so the listing cannot say
			// which entries are placeholders. Only the filter answers "unreadable
			// metadata", and the tag is checked again on the handle before removal.
			unreadable, err := system.Unreadable(filepath.Join(root, child))
			if err != nil {
				return nil, fmt.Errorf("inspect %s: %w", child, err)
			}
			if unreadable {
				if damaged = append(damaged, Damaged{Path: child, Directory: entry.Directory}); len(damaged) > maxDamaged {
					return nil, ErrTooLarge
				}
				continue
			}
			if entry.Directory && (!entry.Reparse || entry.Cloud) {
				pending = append(pending, child)
			}
		}
	}
	sort.Slice(damaged, func(i, j int) bool { return damaged[i].Path < damaged[j].Path })
	return damaged, nil
}

// Repair is the whole elevated operation for one folder.
func Repair(system System, root, run string) Result {
	result := Result{Schema: Schema, Run: run, Root: root}
	fail := func(code string, err error) Result {
		result.Error = code
		if err != nil {
			result.Detail = err.Error()
		}
		return result
	}
	volume, err := volumeOf(root)
	if err != nil {
		return fail("refused_path", err)
	}
	result.Volume = volume
	provider, err := system.SyncRootProvider(root)
	if err != nil {
		return fail("not_anchor", err)
	}
	if provider != Provider {
		return fail("not_anchor", ErrNotAnchor)
	}
	damaged, err := Scan(system, root)
	if err != nil {
		return fail("scan", err)
	}
	result.Found = len(damaged)
	if len(damaged) == 0 {
		return result
	}
	if err := system.Detach(volume); err != nil {
		if errors.Is(err, ErrElevationNeeded) {
			return fail("elevation_required", err)
		}
		return fail("detach", err)
	}
	result.Detached = true
	// Deleting never recurses: a damaged directory that is not empty stays
	// and is reported.
	for index := len(damaged) - 1; index >= 0; index-- {
		entry := damaged[index].Path
		if err := system.RemoveCloudEntry(root, filepath.Join(root, entry), damaged[index].Directory); err != nil {
			result.Remaining = append(result.Remaining, entry)
			if result.Detail == "" {
				result.Detail = fmt.Sprintf("%s: %v", entry, err)
			}
			continue
		}
		result.Removed = append(result.Removed, entry)
	}
	sort.Strings(result.Removed)
	sort.Strings(result.Remaining)
	if err := system.Attach(volume); err != nil {
		result.Error = "attach"
		result.Detail = err.Error()
		return result
	}
	result.Reattached = true
	if len(result.Remaining) > 0 {
		result.Error = "remove"
	}
	return result
}

// Reattach is the second half alone, for a repair that was interrupted after
// the filter was detached.
func Reattach(system System, root, run string) Result {
	result := Result{Schema: Schema, Run: run, Root: root}
	volume, err := volumeOf(root)
	if err != nil {
		result.Error, result.Detail = "refused_path", err.Error()
		return result
	}
	result.Volume = volume
	if err := system.Attach(volume); err != nil {
		result.Error, result.Detail = "attach", err.Error()
		if errors.Is(err, ErrElevationNeeded) {
			result.Error = "elevation_required"
		}
		return result
	}
	result.Reattached = true
	return result
}
