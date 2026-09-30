package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDisplayHealthOnlyConfirmedBrowserLoss(t *testing.T) {
	now := time.Now()
	var health displayHealth
	if health.lost(now, false, nil) || health.lost(now.Add(time.Hour), false, nil) {
		t.Fatal("unobserved startup browser is not loss")
	}
	if health.lost(now, true, nil) || health.lost(now, false, nil) {
		t.Fatal("must wait for grace")
	}
	if health.lost(now.Add(19*time.Second), false, nil) {
		t.Fatal("too early")
	}
	if !health.lost(now.Add(20*time.Second), false, nil) {
		t.Fatal("continuous loss not detected")
	}
	if health.lost(now.Add(21*time.Second), true, nil) {
		t.Fatal("browser returned")
	}
	if health.lost(now.Add(22*time.Second), false, nil) {
		t.Fatal("transient return resets grace")
	}
	if health.lost(now.Add(40*time.Second), false, errors.New("snapshot failed")) {
		t.Fatal("failed probe cannot prove loss")
	}
	if health.lost(now.Add(41*time.Second), false, nil) {
		t.Fatal("failed probe must reset grace")
	}
}

func TestDisplayRecoveryBudgetSurvivesRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	now := time.Now()
	for attempt := 0; attempt < 2; attempt++ {
		if err := takeDisplayRecovery(path, now.Add(time.Duration(attempt)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if err := takeDisplayRecovery(path, now.Add(2*time.Minute)); err == nil {
		t.Fatal("third restart allowed")
	}
	if err := takeDisplayRecovery(path, now.Add(displayRecoveryWindow)); err != nil {
		t.Fatal(err)
	}
	if err := takeDisplayRecovery(path, now); err == nil {
		t.Fatal("backwards clock allowed")
	}
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := takeDisplayRecovery(path, now); err == nil {
		t.Fatal("corrupt state reset budget")
	}
}

func TestDisplayLogIsBounded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gui-display.jsonl")
	if err := os.WriteFile(path, make([]byte, 1<<20), 0600); err != nil {
		t.Fatal(err)
	}
	recordDisplayHealth(dir, "browser_missing", "cause unknown")
	info, err := os.Stat(path)
	if err != nil || info.Size() > 1024 {
		t.Fatalf("rotation failed: %v %v", info, err)
	}
	if _, err := os.Stat(path + ".previous"); err != nil {
		t.Fatal(err)
	}
	// Windows must replace an existing backup, not silently stop logging.
	if err := os.WriteFile(path, make([]byte, 1<<20), 0600); err != nil {
		t.Fatal(err)
	}
	recordDisplayHealth(dir, "recovery_requested", "GUI only")
	info, err = os.Stat(path)
	if err != nil || info.Size() > 1024 {
		t.Fatalf("second rotation failed: %v %v", info, err)
	}
}

func TestDisplayRecoveryAdmissionProtectsDirectCalls(t *testing.T) {
	service := &GUIService{}
	finish, err := service.beginDisplayCall()
	if err != nil {
		t.Fatal(err)
	}
	if service.actionAdmission.TryLock() {
		service.actionAdmission.Unlock()
		t.Fatal("recovery admitted during a direct RPC")
	}
	finish()
	if !service.actionAdmission.TryLock() {
		t.Fatal("finished call still holds admission")
	}
	service.recoveringDisplay = true
	service.actionAdmission.Unlock()
	if _, err := service.beginDisplayCall(); err == nil {
		t.Fatal("direct call accepted during recovery")
	}
	if service.Trigger(ActionRequest{}).Accepted {
		t.Fatal("gesture accepted during recovery")
	}
	begin := service.beginDisplayCall
	history := &TimeMachineService{beginDisplayCall: begin}
	// A nil daemon would panic if admission did not reject the call first.
	if _, err := history.Confirm("synthetic"); err == nil {
		t.Fatal("history mutation was not rejected")
	}
	head := &HeadBrowserService{beginDisplayCall: begin}
	if _, err := head.Materialize("synthetic"); err == nil {
		t.Fatal("materialize mutation was not rejected")
	}
	if _, err := head.Fill(); err == nil {
		t.Fatal("fill mutation was not rejected")
	}
}
