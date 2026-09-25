import test from "node:test";
import assert from "node:assert/strict";
import {problemCardHTML} from "../frontend/problem-card.js";

const t = (key, args = {}) => `${key}${Object.keys(args).length ? JSON.stringify(args) : ""}`;
const esc = value => String(value).replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;").replaceAll('"', "&quot;");

test("the owner gets the explanation and the one remedy button", () => {
  const html = problemCardHTML({kind: "borrow_pending", path: "01/<A>.dwg", more: 2, code: "LOCK-2104", reason: "Usługa niedostępna", remedy: "disable_editing_lock", action_id: "editing_policy"}, t, esc);
  assert.match(html, /problem\.borrowPending\.title/);
  assert.match(html, /01\/&lt;A&gt;\.dwg/);
  assert.match(html, /problem\.borrowPending\.more\{&quot;count&quot;:2\}/);
  assert.match(html, /problem\.reason.*LOCK-2104/);
  assert.match(html, /problem\.borrowPending\.remedyOwner/);
  assert.match(html, /data-repository-action="editing_policy"/);
});

test("a guest is told whom to ask and gets no button", () => {
  const html = problemCardHTML({kind: "borrow_pending", path: "a.dwg", code: "LOCK-2106", remedy: "ask_owner"}, t, esc);
  assert.match(html, /problem\.code.*LOCK-2106/);
  assert.match(html, /problem\.borrowPending\.remedyGuest/);
  assert.doesNotMatch(html, /<button/);
});

test("no problem, or one this card does not know, renders nothing", () => {
  assert.equal(problemCardHTML(null, t, esc), "");
  assert.equal(problemCardHTML({kind: "future_kind"}, t, esc), "");
});

test("the card names who else holds the file", () => {
  const html = problemCardHTML({kind: "borrow_pending", path: "a.dwg", holder: "biuro:<jan>", remedy: "ask_owner"}, t, esc);
  assert.match(html, /problem\.heldBy\{&quot;holder&quot;:&quot;biuro:&lt;jan&gt;&quot;\}/);
});
