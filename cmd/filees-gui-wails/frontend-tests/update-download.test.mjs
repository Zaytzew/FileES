import test from "node:test";
import assert from "node:assert/strict";
import {updateDownloadLine} from "../frontend/update-download.js";

const t = (key, args = {}) => `${key}${Object.keys(args).length ? JSON.stringify(args) : ""}`;
const bytes = value => `${value}B`;

test("the background download is shown with its progress", () => {
  assert.equal(updateDownloadLine({state: "available", download: "downloading", downloaded_bytes: 4, download_total: 10}, t, bytes),
    'version.downloading{"downloaded":"4B","total":"10B"}');
  assert.equal(updateDownloadLine({state: "available", download: "downloading"}, t, bytes), "version.downloadingUnknown");
  assert.equal(updateDownloadLine({state: "available", download: "ready", downloaded_bytes: 10, download_total: 10}, t, bytes), "version.downloaded");
  assert.equal(updateDownloadLine({state: "available", download: "failed"}, t, bytes), "version.downloadFailed");
});

test("nothing is said without an available release or a download", () => {
  assert.equal(updateDownloadLine(null, t, bytes), "");
  assert.equal(updateDownloadLine({state: "available"}, t, bytes), "");
  assert.equal(updateDownloadLine({state: "restart_required", download: "ready"}, t, bytes), "");
});
