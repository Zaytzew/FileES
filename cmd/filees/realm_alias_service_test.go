package main

import "testing"

func TestRealmAliasProjectionKeepsConfirmedAliasAcrossEmptyTick(t *testing.T) {
	service := &realmAliasService{}
	if got := service.ProjectAlias("manual", "realm-acme", ""); got != "" {
		t.Fatalf("initial alias=%q, want empty", got)
	}
	service.rememberConfirmedAlias("manual", "acme")
	if got := service.ProjectAlias("manual", "realm-acme", ""); got != "acme" {
		t.Fatalf("alias after empty tick=%q, want acme", got)
	}
	if got := service.ProjectAlias("manual", "realm-acme", "canonical"); got != "canonical" {
		t.Fatalf("authoritative alias=%q, want canonical", got)
	}
	if got := service.ProjectAlias("manual", "realm-acme", ""); got != "canonical" {
		t.Fatalf("remembered authoritative alias=%q, want canonical", got)
	}
}

func TestRealmAliasProjectionDoesNotCrossRealmBoundary(t *testing.T) {
	service := &realmAliasService{}
	service.ProjectAlias("manual", "old-realm", "old")
	if got := service.ProjectAlias("manual", "new-realm", ""); got != "" {
		t.Fatalf("new realm inherited alias=%q", got)
	}
	service.rememberConfirmedAlias("manual", "new")
	if got := service.ProjectAlias("manual", "new-realm", ""); got != "new" {
		t.Fatalf("new realm confirmed alias=%q, want new", got)
	}
}
