package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDumpJobResumesOnlySameImport(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	opt := dumpFlags{profile: filepath.Join(root, "profile.json"), source: filepath.Join(root, "source.dump"),
		target: filepath.Join(root, "new-wc"), state: state, name: "New repo"}
	job, err := loadOrCreateDumpJob(opt)
	if err != nil {
		t.Fatal(err)
	}
	resume, err := loadOrCreateDumpJob(opt)
	if err != nil || resume.OperationID != job.OperationID {
		t.Fatalf("resume = %+v, %v", resume, err)
	}
	opt.name = "Other repo"
	if _, err := loadOrCreateDumpJob(opt); err == nil {
		t.Fatal("different import reused operation ID")
	}
}

func TestDumpCarrierChecksHeaderAndStoredDigest(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source.dump")
	if err := os.WriteFile(source, []byte("SVN-fs-dump-format-version: 2\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opt := dumpFlags{source: source, state: state}
	job := dumpJob{Schema: dumpJobSchema, OperationID: "00000000-0000-4000-8000-000000000000"}
	if _, err := prepareDumpCarrier(opt, &job); err != nil || job.SHA256 == "" {
		t.Fatalf("carrier = %+v, %v", job, err)
	}
	if err := os.WriteFile(filepath.Join(state, "carrier", "dump.svndump"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareDumpCarrier(opt, &job); err == nil {
		t.Fatal("changed stored dump was accepted")
	}
}
