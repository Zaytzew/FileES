import copy
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import zipfile

SCRIPT = Path(__file__).with_name("store_submission.py")
spec = importlib.util.spec_from_file_location("store_submission", SCRIPT)
s = importlib.util.module_from_spec(spec)
spec.loader.exec_module(s)


def submission(sid="10", version="0.1.1757.0"):
    return {"id": sid, "status": "PendingCommit", "fileUploadUrl": "https://example.test/blob?sig=private",
            "targetPublishMode": "Immediate", "pricing": {"priceId": "Free"},
            "listings": {lang: {"baseListing": {"releaseNotes": "old", "description": "preserve"}}
                         for lang in ("pl-pl", "en-us")},
            "applicationPackages": [{"id": "pkg-old", "fileName": "old.msix", "version": version,
                                      "fileStatus": "Uploaded", "architecture": "x64"}]}


class FakeAPI:
    def __init__(self, pending=None):
        self.base = submission()
        self.live = None
        self.pending = pending
        self.calls = []

    def app(self):
        return {"id": s.IDENTITY["storeId"], "pendingApplicationSubmission":
                {"id": self.pending} if self.pending else None,
                "lastPublishedApplicationSubmission": {"id": "10"}}

    def submission(self, sid):
        return copy.deepcopy(self.live if sid == "11" else self.base)

    def call(self, method, suffix="", payload=None):
        self.calls.append((method, suffix))
        if method == "POST" and suffix == "/submissions":
            self.pending = "11"
            self.live = submission("11")
            return copy.deepcopy(self.live)
        if method == "PUT":
            self.live = copy.deepcopy(payload)
            return copy.deepcopy(payload)
        if suffix.endswith("/commit"):
            self.live["status"] = "CommitStarted"
        return {}


ITEM = {"path": "candidate.msix", "fileName": "candidate.msix", "version": "0.1.1882.0",
        "sha256": "a" * 64, "notes": {"pl-pl": "Nowe", "en-us": "New"}}


class WorkflowTests(unittest.TestCase):
    def test_conflict_and_version_refusal(self):
        with self.assertRaisesRegex(s.Refusal, "Existing draft"):
            s.published_baseline(FakeAPI("99"), FakeAPI("99").app(), ITEM)
        item = {**ITEM, "version": "0.1.1757.0"}
        api = FakeAPI()
        with self.assertRaisesRegex(s.Refusal, "not newer"):
            s.published_baseline(api, api.app(), item)

    def test_metadata_diff_preserves_unrelated_fields(self):
        base = submission()
        wanted = s.proposed(base, ITEM)
        paths = s.approved_changes(base, wanted)
        self.assertEqual(wanted["pricing"], base["pricing"])
        self.assertEqual(wanted["listings"]["pl-pl"]["baseListing"]["description"], "preserve")
        self.assertIn("/targetPublishMode", paths)
        wanted["pricing"]["priceId"] = "Tier2"
        with self.assertRaisesRegex(s.Refusal, "Unexpected metadata"):
            s.approved_changes(base, wanted)

    def test_prepare_retry_upload_and_submit_guard(self):
        api = FakeAPI()
        with tempfile.TemporaryDirectory() as temp:
            state_path = Path(temp) / "state.json"
            with patch.object(s, "upload_zip", side_effect=[OSError("interrupted"), None]) as upload:
                with self.assertRaises(OSError):
                    s.prepare(api, ITEM, state_path)
                state = s.load_state(state_path)
                self.assertEqual(state["submissionId"], "11")
                self.assertEqual(state["stage"], "metadata-updated")
                self.assertNotIn("fileUploadUrl", state["createdSnapshot"])
                s.prepare(api, ITEM, state_path)
                self.assertEqual(upload.call_count, 2)
            self.assertEqual([c for c in api.calls if c[1] == "/submissions"], [("POST", "/submissions")])
            state = s.load_state(state_path)
            with self.assertRaisesRegex(s.Refusal, "differs"):
                s.submit(api, state_path, "11", ITEM["version"], "bad")
            api.live["pricing"]["priceId"] = "Tier2"
            with self.assertRaisesRegex(s.Refusal, "Live metadata"):
                s.submit(api, state_path, "11", ITEM["version"], s.summary(state))
            api.live["pricing"]["priceId"] = "Free"
            s.submit(api, state_path, "11", ITEM["version"], s.summary(state))
            self.assertEqual(s.load_state(state_path)["stage"], "commit-requested")

    def test_create_intent_refuses_unknown_retry(self):
        api = FakeAPI("11")
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "state.json"
            s.save_state(path, {"candidate": {"sha256": ITEM["sha256"], "version": ITEM["version"],
                                                 "fileName": ITEM["fileName"]}, "stage": "create-intent"})
            with self.assertRaisesRegex(s.Refusal, "reconcile manually"):
                s.prepare(api, ITEM, path)
            self.assertFalse(api.calls)

    def test_identity_hash_and_redaction(self):
        with self.assertRaisesRegex(s.Refusal, "SHA-256"):
            s.candidate(s.ROOT / "dist/store-r1882/FileES-Desktop-0.1.18.1882-unsigned.msix",
                        "0" * 64, s.ROOT / "dist/store-r1882/whats-new-pl-en.txt")
        self.assertNotIn("sig=secret", s.safe_error(Exception("https://blob.test/a?sig=secret")))
        self.assertNotIn("abc", s.safe_error(Exception("Bearer abc")))

    def test_wrong_package_identity_and_cfapi_refused(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "wrong.msix"
            notes = Path(temp) / "notes.txt"
            notes.write_text("PL\nNowe\nEN\nNew\n", encoding="utf-8")
            manifest = ('<Package xmlns="http://schemas.microsoft.com/appx/manifest/foundation/windows10">'
                        '<Identity Name="Wrong.App" Publisher="CN=wrong" ProcessorArchitecture="x64" Version="0.1.1882.0"/>'
                        '</Package>')
            with zipfile.ZipFile(path, "w") as archive:
                archive.writestr("AppxManifest.xml", manifest)
                archive.writestr("filees.exe", b"no cloud marker")
            with self.assertRaisesRegex(s.Refusal, "identity"):
                s.candidate(path, s.sha256(path), notes)
            with zipfile.ZipFile(path, "w") as archive:
                archive.writestr("AppxManifest.xml", manifest)
                archive.writestr("filees-cfapi-helper.exe", b"x")
            with self.assertRaisesRegex(s.Refusal, "CFAPI"):
                s.candidate(path, s.sha256(path), notes)

    def test_unknown_copy_change_and_sas_host_refused(self):
        base = submission()
        created = submission("11")
        created["pricing"]["priceId"] = "Tier2"
        self.assertIn("/pricing/priceId", s.differences(s.editable_view(base), s.editable_view(created)))
        with self.assertRaisesRegex(s.Refusal, "valid upload URL"):
            s.upload_zip("https://attacker.example/blob?sig=x", "unused", tempfile.gettempdir())

    def test_server_copy_change_stops_before_update(self):
        class ChangedCopy(FakeAPI):
            def call(self, method, suffix="", payload=None):
                result = super().call(method, suffix, payload)
                if method == "POST" and suffix == "/submissions":
                    self.live["pricing"]["priceId"] = "Tier2"
                    result["pricing"]["priceId"] = "Tier2"
                return result

        api = ChangedCopy()
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "state.json"
            with self.assertRaisesRegex(s.Refusal, "differs from published metadata"):
                s.prepare(api, ITEM, path)
            self.assertEqual(s.load_state(path)["submissionId"], "11")
            self.assertFalse(any(method == "PUT" for method, _ in api.calls))


if __name__ == "__main__":
    unittest.main()
