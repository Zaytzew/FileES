package actions

import (
	"errors"
	"strings"
	"testing"
)

func TestUpdatePlanPresentationRetainsOpaqueValues(t *testing.T) {
	if updatePlanPresentation(nil)["missing"] != "true" {
		t.Fatal("missing plan")
	}
	plan := &UpdatePlan{CurrentVersion: "r1", AvailableVersion: "r2", ReleaseID: "{release}", RestartRequired: true,
		Changes: []UpdateChange{{Action: "put", Path: "<plik>.exe", Detail: "raw {current} diagnostyka"}}}
	args := updatePlanPresentation(plan)
	if args["release"] != "{release}" || args["restart"] != "true" || args["changes"] != "• PUT  <plik>.exe — raw {current} diagnostyka" {
		t.Fatal(args)
	}
	args["release"] = "changed"
	if plan.ReleaseID != "{release}" {
		t.Fatal("presentation changed plan")
	}
}

func TestLocalizedErrorTitlesNeverRewriteDiagnosticBody(t *testing.T) {
	localize := func(key, fallback string) string {
		if key == "error.operation" {
			return "Failed (%s)"
		}
		return key
	}
	c := polishController(t, Config{Text: localize})
	title, body, _ := c.operationErrorPresentation("lock", errors.New("surowa diagnostyka <path>"), localize)
	if title != "Failed (lock)" || body != "surowa diagnostyka <path>" {
		t.Fatal(title, body)
	}
	title, body, _ = c.publishPresentation(errors.New("literal {body}"), localize)
	if title != "error.publish" || body != "literal {body}" {
		t.Fatal(title, body)
	}
	if c.uiText("test", "fallback") != "test" {
		t.Fatal("callback ignored")
	}
	c.cfg.Text = nil
	if c.uiText("test", "fallback") != "fallback" {
		t.Fatal("fallback ignored")
	}
	if !strings.Contains(c.remainingHoursText(5), "5") {
		t.Fatal("duration lost")
	}
}
