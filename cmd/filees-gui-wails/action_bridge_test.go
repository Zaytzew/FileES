package main

import (
	"errors"
	"testing"

	contract "filees/pkg/contract/v1"
)

func TestValidateSystemLifecycleResult(t *testing.T) {
	if err := validateSystemLifecycleResult(&contract.SystemLifecycleResult{Action: "restart"}, "restart", nil); err != nil {
		t.Fatalf("valid result rejected: %v", err)
	}
	if err := validateSystemLifecycleResult(nil, "restart", nil); err == nil {
		t.Fatal("empty result accepted")
	}
	if err := validateSystemLifecycleResult(&contract.SystemLifecycleResult{Action: "shutdown"}, "restart", nil); err == nil {
		t.Fatal("unexpected action accepted")
	}
	want := errors.New("ipc failed")
	if got := validateSystemLifecycleResult(nil, "restart", want); !errors.Is(got, want) {
		t.Fatalf("transport error = %v, want %v", got, want)
	}
}
