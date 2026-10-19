//go:build windows && !nocfapi

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"filees/pkg/client"
	"filees/pkg/filepolicy"
	"filees/pkg/localrepo"
	"filees/pkg/privatefile"
	"filees/pkg/talk"
	"github.com/google/uuid"
	"golang.org/x/sys/windows"
)

type conversionNativeClient interface {
	client.Client
	client.ConversionInventoryReader
	client.ConversionBoundReader
	client.HistoryTreeReader
}
type conversionFSPlan struct {
	Schema, Operation, Attachment, URL, UUID, SourceID string
	Revision                                           int64
	Objects                                            []conversionObject
	Remote                                             []client.HistoryTreeNode
}
type conversionPointReceipt struct {
	Operation, ID                                         string
	Checkout, Registered, CleanupStarted, CleanupComplete bool
	MetadataPending                                       bool // identity persisted before applying the original root metadata
}
type conversionCopyReceipt struct{ Operation, Path, Stage, ID, Phase string }
type conversionDeleteReceipt struct {
	Operation string
	Object    conversionObject
	Done      bool
}

type nativeConversionEffects struct {
	manager      *anchorManager
	record       localrepo.Record
	svn          conversionNativeClient
	directory    string
	plan         conversionFSPlan
	state        conversionPointReceipt
	old          *conversionTreePin
	guards       []windows.Handle
	stopProvider func()
	bridge       *anchorBridge
	admission    func(context.Context) error
	progress     func(string, int64, int64)
	// Test-only fault injection at real per-object receipt boundaries; nil in production.
	checkpoint func(string) error
}

func (f *nativeConversionEffects) bindConversionAdmission(check func(context.Context) error) {
	f.admission = check
}
func (f *nativeConversionEffects) bindConversionProgress(observe func(string, int64, int64)) {
	f.progress = observe
}
func (f *nativeConversionEffects) reportProgress(phase string, done, total int64) {
	if f.progress != nil {
		f.progress(phase, done, total)
	}
}
func (f *nativeConversionEffects) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.admission == nil {
		return errors.New("conversion native effects lack lifecycle admission")
	}
	return f.admission(ctx)
}

func wireNativeConversion(p *daemonProvisioner, m *anchorManager) {
	p.newConversionEffects = func(record localrepo.Record, intent localrepo.ConversionRecord) (conversionEffectsAdapter, error) {
		svn, err := m.svn(record.ServerID)
		if err != nil {
			return nil, err
		}
		cli, ok := svn.(conversionNativeClient)
		if !ok {
			return nil, errors.New("native SVN cannot provide bound conversion operations")
		}
		return &nativeConversionEffects{manager: m, record: record, svn: cli, directory: filepath.Join(p.conversionRoot, intent.ID)}, nil
	}
}
func (f *nativeConversionEffects) cut(label string) error {
	if f.checkpoint != nil {
		return f.checkpoint(label)
	}
	return nil
}
func (f *nativeConversionEffects) receipt(name string) string {
	return filepath.Join(f.directory, name+".json")
}
func (f *nativeConversionEffects) saveState(r localrepo.ConversionRecord) error {
	f.state.Operation = r.ID
	return conversionWriteJSON(f.receipt("point"), f.state, false)
}

func (f *nativeConversionEffects) Acquire(ctx context.Context, r localrepo.ConversionRecord) (func(), error) {
	if f.record.OperationID != r.AttachmentID || f.record.LocalPath != r.SourcePath || filepath.Dir(r.SourcePath) != filepath.Dir(r.BackupPath) || filepath.Clean(f.directory) != f.directory {
		return nil, errors.New("conversion adapter binding differs")
	}
	inside := func(root, target string) bool {
		rel, err := filepath.Rel(root, target)
		return err == nil && (rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
	}
	for _, root := range []string{r.SourcePath, r.BackupPath} {
		if inside(root, f.directory) || inside(f.directory, root) {
			return nil, errors.New("conversion evidence directory overlaps working-copy custody")
		}
	}
	var err error
	f.guards, err = guardAnchorReleaseDirectories(filepath.Dir(r.SourcePath), "")
	if err != nil {
		return nil, err
	}
	release := func() {
		if f.stopProvider != nil {
			f.stopProvider()
			f.stopProvider = nil
		}
		f.bridge = nil
		f.old.close()
		f.old = nil
		closeAnchorReleaseGuards(f.guards)
		f.guards = nil
	}
	if err = os.MkdirAll(f.directory, 0700); err != nil {
		release()
		return nil, err
	}
	private, err := guardAnchorReleaseDirectories(f.directory, "")
	if err != nil {
		release()
		return nil, err
	}
	f.guards = append(f.guards, private...)
	if err = privatefile.Harden(f.directory); err != nil {
		release()
		return nil, err
	}
	err = conversionReadJSON(f.receipt("point"), &f.state)
	if err != nil && !conversionMissing(err) {
		release()
		return nil, err
	}
	if err == nil && f.state.Operation != r.ID {
		release()
		return nil, errors.New("conversion point receipt belongs to another operation")
	}
	return release, ctx.Err()
}

func (f *nativeConversionEffects) Revalidate(ctx context.Context, r localrepo.ConversionRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !r.Prepared {
		return nil
	}
	if r.PlanPath != f.receipt("plan") {
		return errors.New("conversion plan escaped its private operation directory")
	}
	if err := conversionReadJSON(r.PlanPath, &f.plan); err != nil {
		return err
	}
	digest, err := conversionDigest(f.plan)
	if err != nil {
		return err
	}
	if digest != r.PlanSHA256 || f.plan.Schema != "filees.conversion-fs/v1" || f.plan.Operation != r.ID || f.plan.Attachment != r.AttachmentID || f.plan.URL != r.RepoURL || f.plan.SourceID != r.SourceFileID || f.plan.Revision != r.Revision || uuid.Validate(f.plan.UUID) != nil || len(f.plan.Objects) == 0 || len(f.plan.Objects) > 100000 || f.plan.Objects[0].Path != "." || f.plan.Objects[0].ID != r.SourceFileID || !f.plan.Objects[0].Directory {
		return errors.New("conversion immutable plan differs from its journal")
	}
	locals := int64(0)
	seen := map[string]bool{}
	for _, node := range f.plan.Objects {
		if _, err := conversionDecodeStreams(node.Streams); err != nil {
			return err
		}
		if node.Path != "." && (!anchorBatchSafePath(node.Path) && !strings.HasPrefix(node.Path, ".svn/") && !strings.HasPrefix(node.Path, ".filees/") && node.Path != ".svn" && node.Path != ".filees") {
			return errors.New("unsafe conversion plan path")
		}
		if filepath.ToSlash(filepath.Clean(node.Path)) != node.Path || strings.HasPrefix(node.Path, "../") || filepath.IsAbs(node.Path) || seen[strings.ToLower(node.Path)] || node.ID == "" {
			return errors.New("ambiguous conversion plan object")
		}
		seen[strings.ToLower(node.Path)] = true
		if node.Local {
			locals++
		}
	}
	if !seen["."] || locals != r.LocalObjects {
		return errors.New("conversion plan is incomplete")
	}
	return nil
}

func (f *nativeConversionEffects) Prepare(ctx context.Context, r localrepo.ConversionRecord) (localrepo.ConversionPreparation, error) {
	f.reportProgress("inspecting", 0, 0)
	refuse := func(err error) (localrepo.ConversionPreparation, error) {
		return localrepo.ConversionPreparation{}, err
	}
	pin, err := conversionPinTree(ctx, r.SourcePath)
	if err != nil {
		if errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			return refuse(localrepo.ErrConversionWaiting)
		}
		return refuse(err)
	}
	f.old = pin
	inv, err := f.svn.ConversionInventory(ctx, r.SourcePath)
	if err != nil {
		return refuse(err)
	}
	for _, entry := range inv.Entries {
		if entry.Conflicted || entry.Switched || entry.FileExternal || entry.Copied || entry.WCLocked || entry.Versioned && entry.Kind == "dir" && entry.Item == "normal" && entry.Depth != "infinity" {
			return refuse(fmt.Errorf("unsupported conversion state at %q", entry.Path))
		}
	}
	if err = inv.CheckFullClean(); err != nil {
		return refuse(fmt.Errorf("%w: %v", localrepo.ErrConversionWaiting, err))
	}
	if err = validateWorkingCopyIdentity(r.SourcePath, expectedWorkingCopyIdentity(r.ServerID, r.RepoID, r.RepoURL)); err != nil {
		return refuse(err)
	}
	info, err := f.svn.InspectConversionRoot(ctx, r.SourcePath)
	if err != nil {
		return refuse(err)
	}
	remote, err := f.svn.InspectConversionRoot(ctx, r.RepoURL)
	if err != nil {
		return refuse(err)
	}
	if info.URL != r.RepoURL || remote.URL != r.RepoURL || info.UUID != remote.UUID {
		return refuse(errors.New("conversion local and remote repository identities differ"))
	}
	if info.Revision != remote.Revision {
		return refuse(localrepo.ErrConversionWaiting)
	}
	entries := map[string]client.ConversionInventoryEntry{}
	var ignored []string
	for _, entry := range inv.Entries {
		entries[entry.Path] = entry
		if entry.Versioned && (entry.Revision == nil || *entry.Revision != info.Revision) {
			return refuse(localrepo.ErrConversionWaiting)
		}
		// FileES's built-in exclusions need not be svn:ignore properties:
		// our own desktop.ini is normally reported as unversioned. Retain
		// those objects through the same verified local-copy receipts. Never
		// demote an already-versioned path because its name matches a rule.
		if !entry.Versioned && (entry.Item == "ignored" || filepolicy.IsBuiltinIgnored(entry.Path)) {
			ignored = append(ignored, entry.Path)
		}
	}
	plan := conversionFSPlan{Schema: "filees.conversion-fs/v1", Operation: r.ID, Attachment: r.AttachmentID, URL: r.RepoURL, UUID: info.UUID, SourceID: pin.Objects[0].ID, Revision: info.Revision, Objects: pin.Objects}
	listing := filepath.Join(f.directory, "listing-"+uuid.NewString())
	defer os.Remove(listing)
	summary, err := f.svn.HistoryListTree(ctx, r.RepoURL, info.Revision, listing)
	if err != nil {
		return refuse(err)
	}
	if summary.Revision != info.Revision {
		return refuse(errors.New("conversion remote tree revision differs"))
	}
	remoteNodes := map[string]client.HistoryTreeNode{}
	err = client.EachHistoryTreeNode(listing, func(node client.HistoryTreeNode) error {
		if !anchorBatchSafePath(node.Path) || len(plan.Remote) >= 100000 {
			return errors.New("conversion remote tree is unsafe or too large")
		}
		if _, ok := remoteNodes[node.Path]; ok {
			return errors.New("duplicate remote node")
		}
		remoteNodes[node.Path] = node
		plan.Remote = append(plan.Remote, node)
		return nil
	})
	if err != nil {
		return refuse(err)
	}
	var locals int64
	checkedStreamHelper := false
	for i, node := range plan.Objects {
		if !node.Directory && node.Streams != "" && !checkedStreamHelper {
			answer, err := f.manager.call(ctx, "", "version")
			if err != nil || !answer.OK {
				return refuse(errors.Join(err, errors.New("stream metadata helper capability unavailable")))
			}
			for _, feature := range answer.Features {
				if feature == "remote_stream_state_usn_v1" {
					checkedStreamHelper = true
				}
			}
			if !checkedStreamHelper {
				return refuse(errors.New("CFAPI helper lacks remote_stream_state_usn_v1"))
			}
		}
		if node.Directory && node.Streams != "" && (node.Path == "." || entries[node.Path].Versioned) {
			return refuse(fmt.Errorf("conversion does not yet preserve streams on versioned directory %q", node.Path))
		}
		if node.Path == "." || node.Path == ".svn" || strings.HasPrefix(node.Path, ".svn/") || node.Path == ".filees" || strings.HasPrefix(node.Path, ".filees/") {
			continue
		}
		if entry, ok := entries[node.Path]; ok && entry.Versioned {
			n, exists := remoteNodes[node.Path]
			if !exists || (n.Kind == "dir") != node.Directory {
				return refuse(errors.New("clean WC differs from bound repository tree"))
			}
			delete(remoteNodes, node.Path)
			continue
		}
		local := false
		for _, prefix := range ignored {
			if node.Path == prefix || strings.HasPrefix(node.Path, prefix+"/") {
				local = true
				break
			}
		}
		if !local {
			return refuse(localrepo.ErrConversionWaiting)
		}
		if _, collision := remoteNodes[node.Path]; collision {
			return refuse(errors.New("ignored object collides with remote tree"))
		}
		plan.Objects[i].Local = true
		locals++
	}
	if len(remoteNodes) != 0 {
		return refuse(errors.New("conversion source does not contain the complete remote tree"))
	}
	f.plan = plan
	// A plan written before a lost Prepare ACK may be reused only byte-for-byte.
	if err = conversionWriteJSON(f.receipt("plan"), plan, true); errors.Is(err, os.ErrExist) {
		var prior conversionFSPlan
		if err = conversionReadJSON(f.receipt("plan"), &prior); err == nil {
			a, _ := conversionDigest(prior)
			b, _ := conversionDigest(plan)
			if a != b {
				err = errors.New("unacknowledged conversion plan changed")
			}
		}
	}
	if err != nil {
		return refuse(err)
	}
	digest, err := conversionDigest(plan)
	if err != nil {
		return refuse(err)
	}
	return localrepo.ConversionPreparation{SourceFileID: plan.SourceID, Revision: plan.Revision, PlanPath: f.receipt("plan"), PlanSHA256: digest, LocalObjects: locals}, nil
}

func (f *nativeConversionEffects) pinOld(ctx context.Context, r localrepo.ConversionRecord, root string, partial bool) error {
	f.old.close()
	f.old = nil
	pin, err := conversionPinTree(ctx, root)
	if err != nil {
		return err
	}
	expected := map[string]conversionObject{}
	for _, node := range f.plan.Objects {
		expected[node.Path] = node
	}
	for _, got := range pin.Objects {
		want, ok := expected[got.Path]
		if !ok {
			pin.close()
			return errors.New("late object in retained working copy")
		}
		delete(expected, got.Path)
		if !conversionSameObject(got, want) {
			var receipt conversionDeleteReceipt
			validIntent := partial && conversionReadJSON(f.deletePath(want.Path), &receipt) == nil && receipt.Operation == r.ID && receipt.Object == want
			changedRO := got
			changedRO.Attributes = want.Attributes
			if !validIntent || !conversionSameObject(changedRO, want) || got.Attributes&^windows.FILE_ATTRIBUTE_READONLY != want.Attributes&^windows.FILE_ATTRIBUTE_READONLY {
				pin.close()
				return errors.New("retained bytes or identities changed")
			}
		}
	}
	for _, want := range expected {
		var receipt conversionDeleteReceipt
		if !partial || conversionReadJSON(f.deletePath(want.Path), &receipt) != nil || receipt.Operation != r.ID || receipt.Object != want {
			pin.close()
			return errors.New("unreceipted object missing from retained working copy")
		}
	}
	f.old = pin
	return nil
}

func (f *nativeConversionEffects) Retain(ctx context.Context, r localrepo.ConversionRecord) error {
	if err := f.check(ctx); err != nil {
		return err
	}
	if _, err := os.Lstat(r.BackupPath); err == nil {
		if _, err := os.Lstat(r.SourcePath); !conversionMissing(err) {
			return errors.New("both retain paths are occupied")
		}
		return f.pinOld(ctx, r, r.BackupPath, false)
	} else if !conversionMissing(err) {
		return err
	}
	if err := f.pinOld(ctx, r, r.SourcePath, false); err != nil {
		return err
	}
	// NTFS refuses renaming a directory while data/list handles to its
	// descendants are open, even with SHARE_DELETE (native regression).
	// Keep the checked root/parent handles; close descendants, rename, then
	// repin and compare every byte/ID BEFORE creating the new point. A writer
	// entering this gap therefore stops conversion, never authorizes cleanup.
	for rel, pin := range f.old.Files {
		if rel != "." {
			pin.Close()
			delete(f.old.Files, rel)
		}
	}
	for rel, pins := range f.old.Streams {
		conversionCloseStreams(pins)
		delete(f.old.Streams, rel)
	}
	if err := f.cut("retain-unpinned"); err != nil {
		return err
	}
	if err := f.check(ctx); err != nil {
		return err
	}
	if err := conversionRename(f.old.Files["."], r.BackupPath); err != nil {
		return err
	}
	if err := f.pinOld(ctx, r, r.BackupPath, false); err != nil {
		return err
	}
	return f.cut("retained")
}

func (f *nativeConversionEffects) pointRoot(r localrepo.ConversionRecord) ([]windows.Handle, error) {
	if f.state.Operation != r.ID || f.state.ID == "" {
		return nil, errors.New("new point has no ownership receipt")
	}
	guards, err := guardAnchorReleaseDirectories(r.SourcePath, r.SourcePath)
	if err != nil {
		return nil, err
	}
	var info windows.ByHandleFileInformation
	err = windows.GetFileInformationByHandle(guards[len(guards)-1], &info)
	if err == nil && conversionID(info) != f.state.ID {
		err = errors.New("conversion point was replaced")
	}
	if err != nil {
		closeAnchorReleaseGuards(guards)
		return nil, err
	}
	return guards, nil
}

func (f *nativeConversionEffects) Point(ctx context.Context, r localrepo.ConversionRecord) (localrepo.ConversionPoint, error) {
	if err := f.check(ctx); err != nil {
		return localrepo.ConversionPoint{}, err
	}
	if f.state.ID == "" {
		if err := os.Mkdir(r.SourcePath, 0700); err != nil {
			return localrepo.ConversionPoint{}, err
		}
		root, info, err := conversionOpen(r.SourcePath, windows.GENERIC_READ|windows.WRITE_DAC|windows.FILE_WRITE_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE)
		if err != nil {
			return localrepo.ConversionPoint{}, err
		}
		f.state.ID = conversionID(info)
		f.state.MetadataPending = true
		err = f.saveState(r)
		root.Close()
		if err != nil {
			return localrepo.ConversionPoint{}, err
		}
	}
	guards, err := f.pointRoot(r)
	if err != nil {
		return localrepo.ConversionPoint{}, err
	}
	defer closeAnchorReleaseGuards(guards)
	if f.state.MetadataPending {
		root, info, err := conversionOpen(r.SourcePath, windows.GENERIC_READ|windows.WRITE_DAC|windows.FILE_WRITE_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE)
		if err != nil {
			return localrepo.ConversionPoint{}, err
		}
		defer root.Close()
		if conversionID(info) != f.state.ID {
			return localrepo.ConversionPoint{}, errors.New("point identity changed before metadata")
		}
		entries, err := readPinnedMoveDirectory(windows.Handle(root.Fd()), r.SourcePath)
		if err != nil {
			return localrepo.ConversionPoint{}, err
		}
		if len(entries) != 0 {
			return localrepo.ConversionPoint{}, errors.New("new data appeared before point metadata")
		}
		if err = f.cut("point-metadata-pending"); err != nil {
			return localrepo.ConversionPoint{}, err
		}
		if err = conversionSetMetadata(root, f.plan.Objects[0]); err != nil {
			return localrepo.ConversionPoint{}, err
		}
		f.state.MetadataPending = false
		if err = f.saveState(r); err != nil {
			return localrepo.ConversionPoint{}, err
		}
		if err = f.cut("point-metadata-ready"); err != nil {
			return localrepo.ConversionPoint{}, err
		}
	}
	if !f.state.Checkout {
		if _, err := os.Lstat(filepath.Join(r.SourcePath, ".svn")); conversionMissing(err) {
			if _, err = f.svn.CheckoutDepthEmptyAt(ctx, r.RepoURL, r.SourcePath, r.Revision); err != nil {
				return localrepo.ConversionPoint{}, err
			}
		} else if err != nil {
			return localrepo.ConversionPoint{}, err
		}
		if err := f.checkSparse(ctx, r); err != nil {
			return localrepo.ConversionPoint{}, err
		}
		if err := ensureWorkingCopyIdentity(r.SourcePath, expectedWorkingCopyIdentity(r.ServerID, r.RepoID, r.RepoURL)); err != nil {
			return localrepo.ConversionPoint{}, err
		}
		f.state.Checkout = true
		if err := f.saveState(r); err != nil {
			return localrepo.ConversionPoint{}, err
		}
	}
	answer, err := f.manager.call(ctx, "", "info", "--root", r.SourcePath)
	if err != nil {
		return localrepo.ConversionPoint{}, err
	}
	if answer.OK && !answer.Ours {
		return localrepo.ConversionPoint{}, errors.New("conversion point has a foreign provider")
	}
	if !answer.OK {
		if !strings.EqualFold(answer.HResult, "0x80070186") {
			return localrepo.ConversionPoint{}, fmt.Errorf("conversion refuses an unconfirmed provider registration: %s %s", answer.Error, answer.HResult)
		}
		answer, err = f.manager.call(ctx, "", "register", "--root", r.SourcePath, "--identity", r.ServerID+"\x1f"+r.RepoID)
		if err != nil || !answer.OK {
			return localrepo.ConversionPoint{}, errors.Join(err, errors.New("conversion CFAPI registration failed"))
		}
	}
	if !f.state.Registered {
		if err = f.manager.seedAt(ctx, f.recordAt(r), r.Revision); err != nil {
			return localrepo.ConversionPoint{}, err
		}
	}
	f.state.Registered = true
	if err = f.saveState(r); err != nil {
		return localrepo.ConversionPoint{}, err
	}
	if err = f.connect(ctx, r); err != nil {
		return localrepo.ConversionPoint{}, err
	}
	if err = f.cut("point-ready"); err != nil {
		return localrepo.ConversionPoint{}, err
	}
	return localrepo.ConversionPoint{FileID: f.state.ID, ProviderID: "FileES"}, nil
}
func (f *nativeConversionEffects) recordAt(r localrepo.ConversionRecord) localrepo.Record {
	copy := f.record
	copy.LocalPath = r.SourcePath
	copy.Anchor = true
	copy.Sparse = true
	return copy
}
func (f *nativeConversionEffects) checkSparse(ctx context.Context, r localrepo.ConversionRecord) error {
	info, err := f.svn.InspectConversionRoot(ctx, r.SourcePath)
	if err != nil {
		return err
	}
	if info.URL != r.RepoURL || info.UUID != f.plan.UUID || info.Revision != r.Revision {
		return errors.New("new sparse root has another repository baseline")
	}
	inv, err := f.svn.ConversionInventory(ctx, r.SourcePath)
	if err != nil {
		return err
	}
	for _, n := range inv.Entries {
		if n.Versioned && (n.Path != "." || n.Kind != "dir" || n.Depth != "empty" || n.Item != "normal" || n.Props != "normal" && n.Props != "none" || n.Conflicted || n.Copied || n.Switched || n.FileExternal || n.WCLocked) {
			return errors.New("new sparse root changed during conversion")
		}
	}
	return nil
}
func (f *nativeConversionEffects) connect(ctx context.Context, r localrepo.ConversionRecord) error {
	if f.stopProvider != nil {
		return nil
	}
	providerCtx, cancel := context.WithCancel(ctx)
	streams, err := startAnchorHelper(providerCtx, f.manager.helper, r.SourcePath)
	if err != nil {
		cancel()
		return err
	}
	ready, done := make(chan struct{}), make(chan struct{})
	bridge := &anchorBridge{root: r.SourcePath, guard: newAnchorGuard(talk.With("conversion")), log: talk.With("conversion"), onConnected: func(context.Context) { close(ready) }, materialize: func(_ context.Context, identity string) (string, int64, error) {
		_ = f.cut("fetch-request:" + identity)
		return "", 0, errors.New("conversion is still preparing this folder")
	}}
	bridge.stopStateWriter = streams.abort
	go func() { defer close(done); _ = bridge.serve(providerCtx, streams) }()
	stop := func() { cancel(); streams.abort(); <-done }
	select {
	case <-ready:
		f.stopProvider = stop
		f.bridge = bridge
		return nil
	case <-done:
		cancel()
		return errors.New("conversion provider stopped before connection ACK")
	case <-ctx.Done():
		stop()
		return ctx.Err()
	case <-time.After(15 * time.Second):
		stop()
		return errors.New("conversion provider did not acknowledge connection")
	}
}

func (f *nativeConversionEffects) copyPath(rel string) string {
	digest, _ := conversionDigest(rel)
	return f.receipt("copy-" + digest)
}
func (f *nativeConversionEffects) deletePath(rel string) string {
	digest, _ := conversionDigest(rel)
	return f.receipt("delete-" + digest)
}

func (f *nativeConversionEffects) Preserve(ctx context.Context, r localrepo.ConversionRecord) error {
	if err := f.pinOld(ctx, r, r.BackupPath, false); err != nil {
		return fmt.Errorf("pin retained copy before preservation: %w", err)
	}
	rootGuards, err := f.pointRoot(r)
	if err != nil {
		return fmt.Errorf("pin point before preservation: %w", err)
	}
	defer closeAnchorReleaseGuards(rootGuards)
	metadata, err := guardAnchorReleaseDirectories(filepath.Join(r.SourcePath, ".filees"), r.SourcePath)
	if err != nil {
		return fmt.Errorf("pin preservation staging directory: %w", err)
	}
	defer closeAnchorReleaseGuards(metadata)
	var done, total int64
	for _, node := range f.plan.Objects {
		if node.Local || node.Streams != "" {
			total++
		}
	}
	f.reportProgress("preserving", 0, total)
	for _, node := range f.plan.Objects {
		if !node.Local && node.Streams == "" {
			continue
		}
		if err := f.check(ctx); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		target := filepath.Join(r.SourcePath, filepath.FromSlash(node.Path))
		parent := filepath.Dir(target)
		if node.Path == "." {
			parent = r.SourcePath
		}
		parents, err := guardAnchorReleaseDirectories(parent, r.SourcePath)
		if err != nil {
			return fmt.Errorf("pin ignored target parent %q: %w", node.Path, err)
		}
		if node.Local {
			err = f.copyOne(ctx, r, node, target)
		} else {
			err = f.preserveRemoteStreams(ctx, r, node)
		}
		closeAnchorReleaseGuards(parents)
		if err != nil {
			return fmt.Errorf("preserve %q: %w", node.Path, err)
		}
		done++
		f.reportProgress("preserving", done, total)
	}
	return nil
}

func (f *nativeConversionEffects) copyOne(ctx context.Context, r localrepo.ConversionRecord, node conversionObject, target string) error {
	var receipt conversionCopyReceipt
	err := conversionReadJSON(f.copyPath(node.Path), &receipt)
	if err != nil && !conversionMissing(err) {
		return err
	}
	if err == nil && (receipt.Operation != r.ID || receipt.Path != node.Path || receipt.ID == "" || filepath.Dir(receipt.Stage) != filepath.Join(r.SourcePath, ".filees") || (receipt.Phase != "staged" && receipt.Phase != "moving" && receipt.Phase != "complete")) {
		return errors.New("ignored-object receipt has another binding")
	}
	if conversionMissing(err) {
		if _, err := os.Lstat(target); !conversionMissing(err) {
			return errors.New("ignored destination already exists; refusing overwrite")
		}
		stage := filepath.Join(r.SourcePath, ".filees", "conversion-"+uuid.NewString())
		if node.Directory {
			err = os.Mkdir(stage, 0700)
		} else {
			var file *os.File
			file, err = os.OpenFile(stage, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
			if file != nil {
				err = errors.Join(err, file.Close())
			}
		}
		if err != nil {
			return err
		}
		file, info, err := conversionOpen(stage, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE)
		if err != nil {
			return err
		}
		file.Close()
		receipt = conversionCopyReceipt{Operation: r.ID, Path: node.Path, Stage: stage, ID: conversionID(info), Phase: "staged"}
		if err = conversionWriteJSON(f.copyPath(node.Path), receipt, true); err != nil {
			return err
		}
	}
	if receipt.Phase == "complete" || receipt.Phase == "moving" {
		file, info, err := conversionOpen(target, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE)
		if err == nil {
			defer file.Close()
			if conversionID(info) != receipt.ID || (info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != node.Directory {
				return errors.New("ignored destination was replaced")
			}
			if err := conversionCheckMetadata(file, info, node); err != nil {
				return err
			}
			if err := conversionCheckStreams(ctx, file, node.Streams); err != nil {
				return err
			}
			if !node.Directory {
				hash, err := conversionFileHash(ctx, file, node.Size)
				if err != nil {
					return err
				}
				if hash != node.SHA256 {
					return errors.New("ignored destination bytes changed")
				}
			}
			if _, err := os.Lstat(receipt.Stage); !conversionMissing(err) {
				return errors.New("duplicate ignored-object custody")
			}
			receipt.Phase = "complete"
			return conversionWriteJSON(f.copyPath(node.Path), receipt, false)
		} else if receipt.Phase == "complete" || !conversionMissing(err) {
			return err
		}
	}
	// Only the recorded private staging object's FileID permits partial rewrite.
	access := uint32(windows.GENERIC_READ | windows.DELETE | windows.WRITE_DAC | windows.FILE_WRITE_ATTRIBUTES)
	if receipt.Phase == "staged" {
		access |= windows.GENERIC_WRITE
	}
	file, info, err := conversionOpen(receipt.Stage, access, windows.FILE_SHARE_READ)
	if err != nil {
		return err
	}
	defer file.Close()
	if conversionID(info) != receipt.ID || (info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != node.Directory {
		return errors.New("ignored staging object changed")
	}
	if receipt.Phase == "staged" && !node.Directory {
		if err = file.Truncate(0); err != nil {
			return err
		}
		source := f.old.Files[node.Path]
		if source == nil {
			return errors.New("ignored source is not pinned")
		}
		if _, err = source.Seek(0, io.SeekStart); err != nil {
			return err
		}
		hash, err := hashAnchorRelease(ctx, source, file, node.Size)
		if err != nil {
			return err
		}
		if hash != node.SHA256 {
			return errors.New("ignored source differs from plan")
		}
		if err = file.Sync(); err != nil {
			return err
		}
	}
	if receipt.Phase == "staged" {
		if err = f.copyLocalStreams(ctx, node, file); err != nil {
			return err
		}
		if err = conversionSetMetadata(file, node); err != nil {
			return err
		}
	} else {
		if err = conversionCheckStreams(ctx, file, node.Streams); err != nil {
			return err
		}
		if err = conversionCheckMetadata(file, info, node); err != nil {
			return err
		}
		if !node.Directory {
			hash, err := conversionFileHash(ctx, file, node.Size)
			if err != nil || hash != node.SHA256 {
				return errors.Join(err, errors.New("ignored staging bytes changed"))
			}
		}
	}
	receipt.Phase = "moving"
	if err = conversionWriteJSON(f.copyPath(node.Path), receipt, false); err != nil {
		return err
	}
	if err = f.cut("copy-ready:" + node.Path); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = conversionRename(file, target); err != nil {
		return fmt.Errorf("publish staged ignored object: %w", err)
	}
	if err = f.cut("copy-moved:" + node.Path); err != nil {
		return err
	}
	receipt.Phase = "complete"
	return conversionWriteJSON(f.copyPath(node.Path), receipt, false)
}

func (f *nativeConversionEffects) Verify(ctx context.Context, r localrepo.ConversionRecord) error {
	guards, err := f.pointRoot(r)
	if err != nil {
		return err
	}
	defer closeAnchorReleaseGuards(guards)
	if r.PointFileID != f.state.ID || r.ProviderID != "FileES" {
		return errors.New("new point identity differs from journal")
	}
	if err = f.checkSparse(ctx, r); err != nil {
		return err
	}
	info, err := f.svn.InspectConversionRoot(ctx, r.RepoURL)
	if err != nil {
		return err
	}
	if info.UUID != f.plan.UUID || info.URL != r.RepoURL || info.Revision < r.Revision {
		return errors.New("repository identity or retained history changed")
	}
	if err = f.connect(ctx, r); err != nil {
		return err
	}
	answer, err := f.manager.call(ctx, "", "info", "--root", r.SourcePath)
	if err != nil || !answer.OK || !answer.Ours || answer.Status != 1 {
		return errors.Join(err, errors.New("new CFAPI provider is not connected and idle"))
	}
	if err = f.acknowledgeStreamMetadata(ctx, r); err != nil {
		return err
	}
	for start := 0; start < len(f.plan.Remote); start += 64 {
		end := start + 64
		if end > len(f.plan.Remote) {
			end = len(f.plan.Remote)
		}
		var input strings.Builder
		for _, node := range f.plan.Remote[start:end] {
			input.WriteString(node.Path + "\n")
		}
		states, err := f.manager.call(ctx, input.String(), "tree-states", "--root", r.SourcePath)
		if err != nil || !states.OK || len(states.States) != end-start {
			return errors.Join(err, errors.New("conversion placeholder observation incomplete"))
		}
		for i, state := range states.States {
			node := f.plan.Remote[start+i]
			if !state.OK || !state.Placeholder || !state.InSync || state.Identity != node.Path || state.Directory != (node.Kind == "dir") || node.Kind == "file" && (state.Local || state.OnDisk != 0 || state.Modified != 0 || state.Size == nil || *state.Size != node.Size) {
				return fmt.Errorf("conversion placeholder %q differs: %+v", node.Path, state)
			}
		}
	}
	for _, node := range f.plan.Objects {
		if !node.Local {
			continue
		}
		parents, guardErr := guardAnchorReleaseDirectories(filepath.Dir(filepath.Join(r.SourcePath, filepath.FromSlash(node.Path))), r.SourcePath)
		if guardErr != nil {
			return guardErr
		}
		defer closeAnchorReleaseGuards(parents)
		var receipt conversionCopyReceipt
		if err := conversionReadJSON(f.copyPath(node.Path), &receipt); err != nil {
			return err
		}
		if receipt.Operation != r.ID || receipt.Path != node.Path || receipt.Phase != "complete" {
			return errors.New("ignored object preservation is incomplete")
		}
		file, info, err := conversionOpen(filepath.Join(r.SourcePath, filepath.FromSlash(node.Path)), windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE)
		if err != nil {
			return err
		}
		if conversionID(info) != receipt.ID {
			file.Close()
			return errors.New("preserved local object was replaced")
		}
		if err := conversionCheckMetadata(file, info, node); err != nil {
			file.Close()
			return err
		}
		if err := conversionCheckStreams(ctx, file, node.Streams); err != nil {
			file.Close()
			return err
		}
		if !node.Directory {
			hash, err := conversionFileHash(ctx, file, node.Size)
			if err != nil || hash != node.SHA256 {
				file.Close()
				return errors.Join(err, errors.New("preserved ignored bytes changed"))
			}
		}
		file.Close()
	}
	streamPins, err := f.pinPreservedStreams(ctx, r)
	if err != nil {
		return err
	}
	conversionCloseStreams(streamPins)
	if f.state.CleanupComplete {
		if _, err := os.Lstat(r.BackupPath); !conversionMissing(err) {
			return errors.New("retired root reappeared")
		}
		return nil
	}
	if _, err := os.Lstat(r.BackupPath); conversionMissing(err) {
		var receipt conversionDeleteReceipt
		if !f.state.CleanupStarted || conversionReadJSON(f.deletePath("."), &receipt) != nil || receipt.Operation != r.ID || receipt.Object != f.plan.Objects[0] {
			return errors.New("retained root disappeared without retirement intent")
		}
		return nil
	}
	return f.pinOld(ctx, r, r.BackupPath, f.state.CleanupStarted)
}

func (f *nativeConversionEffects) Cleanup(ctx context.Context, r localrepo.ConversionRecord) error {
	// Keep every preserved local file pinned against a late writer THROUGH all
	// retirements. Never delete source based on an earlier hash-only Verify.
	var pins []*os.File
	rootGuards, err := f.pointRoot(r)
	if err != nil {
		return err
	}
	defer closeAnchorReleaseGuards(rootGuards)
	var parentGuards []windows.Handle
	defer func() { closeAnchorReleaseGuards(parentGuards) }()
	defer func() {
		for _, file := range pins {
			file.Close()
		}
	}()
	streamPins, err := f.pinPreservedStreams(ctx, r)
	if err != nil {
		return err
	}
	defer conversionCloseStreams(streamPins)
	for _, node := range f.plan.Objects {
		if !node.Local {
			continue
		}
		path := filepath.Join(r.SourcePath, filepath.FromSlash(node.Path))
		parents, err := guardAnchorReleaseDirectories(filepath.Dir(path), r.SourcePath)
		if err != nil {
			return err
		}
		parentGuards = append(parentGuards, parents...)
		var receipt conversionCopyReceipt
		if err := conversionReadJSON(f.copyPath(node.Path), &receipt); err != nil {
			return err
		}
		file, info, err := conversionOpen(path, windows.GENERIC_READ, windows.FILE_SHARE_READ)
		if err != nil {
			return err
		}
		pins = append(pins, file)
		if receipt.Operation != r.ID || receipt.Path != node.Path || receipt.Phase != "complete" || conversionID(info) != receipt.ID || (info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != node.Directory {
			return errors.New("ignored target changed before retirement")
		}
		if err := conversionCheckMetadata(file, info, node); err != nil {
			return err
		}
		gotStreams, localStreamPins, err := conversionPinStreams(ctx, file)
		pins = append(pins, localStreamPins...)
		if err != nil {
			return err
		}
		if gotStreams != node.Streams {
			return errors.New("preserved local streams changed before cleanup")
		}
		if !node.Directory {
			hash, err := conversionFileHash(ctx, file, node.Size)
			if err != nil || hash != node.SHA256 {
				return errors.Join(err, errors.New("ignored destination changed before cleanup"))
			}
		}
	}
	if !f.state.CleanupStarted {
		f.state.CleanupStarted = true
		if err := f.saveState(r); err != nil {
			return err
		}
	}
	objects := append([]conversionObject(nil), f.plan.Objects...)
	sort.Slice(objects, func(i, j int) bool {
		di, dj := strings.Count(objects[i].Path, "/"), strings.Count(objects[j].Path, "/")
		if objects[i].Path == "." {
			return false
		}
		if objects[j].Path == "." {
			return true
		}
		if di != dj {
			return di > dj
		}
		return objects[i].Path > objects[j].Path
	})
	for index, node := range objects {
		if err := f.check(ctx); err != nil {
			return err
		}
		var receipt conversionDeleteReceipt
		err := conversionReadJSON(f.deletePath(node.Path), &receipt)
		if err != nil && !conversionMissing(err) {
			return err
		}
		if err == nil && (receipt.Operation != r.ID || receipt.Object != node) {
			return errors.New("retirement receipt changed")
		}
		path := filepath.Join(r.BackupPath, filepath.FromSlash(node.Path))
		if _, statErr := os.Lstat(path); conversionMissing(statErr) {
			if err != nil {
				return errors.New("missing source object without retirement intent")
			}
		} else {
			if statErr != nil {
				return statErr
			}
			if receipt.Done {
				return errors.New("retired object reappeared")
			}
			if err != nil {
				receipt = conversionDeleteReceipt{Operation: r.ID, Object: node}
				if err = conversionWriteJSON(f.deletePath(node.Path), receipt, true); err != nil {
					return err
				}
			}
			if err = f.cut("delete-intent:" + node.Path); err != nil {
				return err
			}
			if err = f.check(ctx); err != nil {
				return err
			}
			if f.old != nil {
				conversionCloseStreams(f.old.Streams[node.Path])
				delete(f.old.Streams, node.Path)
				if pin := f.old.Files[node.Path]; pin != nil {
					pin.Close()
					delete(f.old.Files, node.Path)
				}
			}
			if err = conversionDelete(ctx, path, node); err != nil {
				return err
			}
			if err = f.cut("deleted:" + node.Path); err != nil {
				return err
			}
		}
		receipt.Done = true
		if err = conversionWriteJSON(f.deletePath(node.Path), receipt, false); err != nil {
			return err
		}
		f.reportProgress("cleanup", int64(index+1), int64(len(objects)))
	}
	f.state.CleanupComplete = true
	return f.saveState(r)
}

func (f *nativeConversionEffects) Rollback(ctx context.Context, r localrepo.ConversionRecord) (string, error) {
	if f.stopProvider != nil {
		f.stopProvider()
		f.stopProvider = nil
	}
	f.old.close()
	f.old = nil
	if f.state.ID != "" {
		if guards, err := f.pointRoot(r); err == nil {
			answer, callErr := f.manager.call(ctx, "", "info", "--root", r.SourcePath)
			if callErr != nil {
				closeAnchorReleaseGuards(guards)
				return "", callErr
			}
			if answer.OK {
				if !answer.Ours {
					closeAnchorReleaseGuards(guards)
					return "", errors.New("rollback refuses a foreign provider")
				}
				answer, callErr = f.manager.call(ctx, "", "unregister", "--root", r.SourcePath)
				if callErr != nil || !answer.OK {
					closeAnchorReleaseGuards(guards)
					return "", errors.Join(callErr, errors.New("rollback could not unregister its point"))
				}
			}
			closeAnchorReleaseGuards(guards)
		}
	}
	if root, info, err := conversionOpen(r.SourcePath, windows.GENERIC_READ|windows.DELETE, windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE); err == nil {
		defer root.Close()
		if conversionID(info) == r.SourceFileID {
			if _, err := os.Lstat(r.BackupPath); !conversionMissing(err) {
				return "", errors.New("duplicate original root during rollback")
			}
			return r.SourceFileID, nil
		}
		if conversionID(info) != f.state.ID {
			return "", errors.New("rollback refuses an occupied foreign source")
		}
		custody := r.BackupPath + "-new-" + r.ID
		if err := conversionRename(root, custody); err != nil {
			return "", err
		}
	} else if !conversionMissing(err) {
		return "", err
	}
	old, info, err := conversionOpen(r.BackupPath, windows.GENERIC_READ|windows.DELETE, windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE)
	if err != nil {
		return "", err
	}
	defer old.Close()
	if conversionID(info) != r.SourceFileID {
		return "", errors.New("rollback original identity differs")
	}
	if err = conversionRename(old, r.SourcePath); err != nil {
		return "", err
	}
	return r.SourceFileID, ctx.Err()
}

func (f *nativeConversionEffects) Finalize(ctx context.Context, r localrepo.ConversionRecord) (func(), error) {
	release, err := f.Acquire(ctx, r)
	if err != nil {
		return nil, err
	}
	if err = f.Revalidate(ctx, r); err != nil {
		release()
		return nil, err
	}
	if r.Phase != localrepo.ConversionComplete {
		file, info, err := conversionOpen(r.SourcePath, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE)
		if err != nil {
			release()
			return nil, err
		}
		if r.Prepared && conversionID(info) != r.SourceFileID {
			file.Close()
			release()
			return nil, errors.New("rollback final root identity differs")
		}
		if err := validateWorkingCopyIdentity(r.SourcePath, expectedWorkingCopyIdentity(r.ServerID, r.RepoID, r.RepoURL)); err != nil {
			file.Close()
			release()
			return nil, err
		}
		return func() { file.Close(); release() }, nil
	}
	guards, err := f.pointRoot(r)
	if err != nil {
		release()
		return nil, err
	}
	if !f.state.CleanupComplete {
		closeAnchorReleaseGuards(guards)
		release()
		return nil, errors.New("conversion retirement was not durably completed")
	}
	if err = f.Verify(ctx, r); err != nil {
		closeAnchorReleaseGuards(guards)
		release()
		return nil, err
	}
	return func() { closeAnchorReleaseGuards(guards); release() }, nil
}

var _ conversionEffectsAdapter = (*nativeConversionEffects)(nil)
