import assert from "node:assert/strict";
import { test } from "node:test";
import { publicShareURL, openPublicShare } from "../frontend/public-share-link.js";

test("recipient URL only accepts HTTPS without credentials or tokens", () => {
  for (const value of [undefined, "", "//host/path", "http://host/r/s", "file:///C:/x", "javascript:alert(1)", "https://user:secret@host/r/s", "https://host/r/s?invite=secret", "https://host/r/s#token", "https://host\\evil/r/s", " https://host/r/s", "https://host/r/\ns"]) {
    assert.equal(publicShareURL(value), "", String(value));
  }
  assert.equal(publicShareURL("https://download.example:8443/realm/share"), "https://download.example:8443/realm/share");
});

test("opening recipient page uses exact share from current dialog and leaves it intact", async () => {
  const snapshot = {mode: "shares", shares: [{channel_id: "one", public_url: "https://download.example/realm/share"}, {channel_id: "missing"}]};
  const before = JSON.stringify(snapshot), calls = [];
  const open = async url => calls.push(url);
  assert.equal(await openPublicShare(snapshot, "one", open), true);
  assert.equal(await openPublicShare(snapshot, "missing", open), false);
  assert.equal(await openPublicShare(snapshot, "unknown", open), false);
  assert.equal(await openPublicShare({...snapshot, mode: "settings"}, "one", open), false);
  assert.deepEqual(calls, ["https://download.example/realm/share"]);
  assert.equal(JSON.stringify(snapshot), before);
  await assert.rejects(openPublicShare(snapshot, "one", async () => { throw new Error("browser unavailable"); }), /browser unavailable/);
});
