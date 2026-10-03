"""Guarded Microsoft Store MSIX submission workflow. No implicit writes."""
import argparse
import copy
import hashlib
import json
import os
from pathlib import Path
import re
import sys
import tempfile
import urllib.error
import urllib.parse
import urllib.request
import xml.etree.ElementTree as ET
import zipfile

ROOT = Path(__file__).resolve().parents[3]
IDENTITY = json.loads((ROOT / "packaging/windows/store-identity.json").read_text(encoding="utf-8"))
API = "https://manage.devcenter.microsoft.com/v1.0/my/applications/" + IDENTITY["storeId"]
DEFAULT_STATE = Path(os.environ.get("LOCALAPPDATA", str(Path.home()))) / "FileES/StoreSubmission/state.json"
SENSITIVE = re.compile(r"(?i)(https://[^\s\"']+\?[^\s\"']+|bearer\s+[^\s]+|client_secret[^\s,}]*|sig=[^&\s]+)")


class Refusal(Exception):
    pass


def safe_error(exc):
    if isinstance(exc, urllib.error.HTTPError):
        return f"HTTP {exc.code} from Microsoft service"
    return SENSITIVE.sub("[REDACTED]", str(exc))


def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def version_tuple(version):
    if not re.fullmatch(r"\d+\.\d+\.\d+\.\d+", str(version)):
        raise Refusal("Invalid four-part MSIX version")
    return tuple(map(int, version.split(".")))


def candidate(path, expected_hash, notes_path):
    path = Path(path).resolve()
    if not path.is_file() or path.suffix.lower() != ".msix":
        raise Refusal("Candidate must be an existing MSIX")
    actual_hash = sha256(path)
    if actual_hash.lower() != expected_hash.lower():
        raise Refusal("MSIX SHA-256 differs from approved value")
    with zipfile.ZipFile(path) as archive:
        names = archive.namelist()
        if any("cfapi" in name.lower() for name in names):
            raise Refusal("CFAPI helper found in Store package")
        manifest = ET.fromstring(archive.read("AppxManifest.xml"))
        ns = "{http://schemas.microsoft.com/appx/manifest/foundation/windows10}"
        identity = manifest.find(ns + "Identity")
        if identity is None or identity.attrib.get("Name") != IDENTITY["identityName"] or identity.attrib.get("Publisher") != IDENTITY["publisher"]:
            raise Refusal("MSIX identity differs from Store identity")
        if identity.attrib.get("ProcessorArchitecture", "").lower() != "x64":
            raise Refusal("Unexpected MSIX architecture")
        version = identity.attrib["Version"]
        version_tuple(version)
        if version.split(".")[-1] != "0":
            raise Refusal("Store MSIX fourth version field must be zero")
        daemon = archive.read("filees.exe").lower()
        if any(marker in daemon for marker in (b"cldapi", b"cfgetplaceholderstate", b"main.exploreranchormanager")):
            raise Refusal("Cloud Files API marker found in Store daemon")
        if not {"filees-store-launcher.exe", "filees-store-startup.exe"}.issubset(names):
            raise Refusal("Store launchers missing")
    notes = Path(notes_path).read_text(encoding="utf-8-sig")
    match = re.fullmatch(r"PL\s*\n(.*?)\s*\nEN\s*\n(.*?)\s*", notes, re.S)
    if not match or not all(match.groups()):
        raise Refusal("Expected nonempty PL and EN release notes")
    return {"path": str(path), "fileName": path.name, "version": version,
            "sha256": actual_hash, "notes": {"pl-pl": match[1].strip(), "en-us": match[2].strip()}}


def scrub(obj):
    if isinstance(obj, dict):
        return {k: scrub(v) for k, v in obj.items() if k.lower() not in {"fileuploadurl", "access_token", "client_secret"}}
    if isinstance(obj, list):
        return [scrub(v) for v in obj]
    return obj


def save_state(path, state):
    path = Path(path).resolve()
    if ROOT.resolve() == path or ROOT.resolve() in path.parents:
        raise Refusal("State must be outside the SVN working copy")
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, temp = tempfile.mkstemp(prefix="state-", dir=path.parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as stream:
            json.dump(scrub(state), stream, ensure_ascii=False, indent=2)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temp, path)
    finally:
        if os.path.exists(temp):
            os.unlink(temp)


def load_state(path):
    return json.loads(Path(path).read_text(encoding="utf-8")) if Path(path).exists() else None


class APIClient:
    def __init__(self, token):
        if not token:
            raise Refusal("No access token. Use the local DPAPI launcher after account setup")
        self.token = token

    def call(self, method, suffix="", payload=None):
        body = None if payload is None else json.dumps(payload, ensure_ascii=False).encode("utf-8")
        request = urllib.request.Request(API + suffix, data=body, method=method,
            headers={"Authorization": "Bearer " + self.token, "Content-Type": "application/json"})
        with urllib.request.urlopen(request, timeout=60) as response:
            data = response.read()
        return json.loads(data) if data else {}

    def app(self):
        app = self.call("GET")
        if app.get("id") != IDENTITY["storeId"] or app.get("packageIdentityName") != IDENTITY["identityName"] or app.get("publisherName") != IDENTITY["publisher"]:
            raise Refusal("Partner Center product identity mismatch")
        return app

    def submission(self, sid):
        if not re.fullmatch(r"\d+", str(sid)):
            raise Refusal("Invalid submission ID")
        return self.call("GET", "/submissions/" + str(sid))


def submission_id(app, name):
    return (app.get(name) or {}).get("id")


def published_baseline(api, app, item):
    if submission_id(app, "pendingApplicationSubmission"):
        raise Refusal("Existing draft or certification in progress; inspect it before any write")
    sid = submission_id(app, "lastPublishedApplicationSubmission")
    if not sid:
        raise Refusal("No published submission to copy")
    baseline = api.submission(sid)
    packages = baseline.get("applicationPackages")
    if not isinstance(packages, list) or len(packages) != 1:
        raise Refusal("Expected exactly one published desktop package")
    old_version = packages[0].get("version")
    if version_tuple(item["version"]) <= version_tuple(old_version):
        raise Refusal(f"Candidate version {item['version']} is not newer than published {old_version}")
    for lang in item["notes"]:
        if lang not in baseline.get("listings", {}) or "baseListing" not in baseline["listings"][lang]:
            raise Refusal(f"Published submission has no {lang} listing")
    return baseline


def proposed(base, item):
    result = copy.deepcopy(base)
    old = result["applicationPackages"][0]
    old["fileStatus"] = "PendingDelete"
    result["applicationPackages"].append({"fileName": item["fileName"], "fileStatus": "PendingUpload"})
    for lang, notes in item["notes"].items():
        result["listings"][lang]["baseListing"]["releaseNotes"] = notes
    result["targetPublishMode"] = "Manual"
    return result


def differences(left, right, prefix=""):
    if isinstance(left, dict) and isinstance(right, dict):
        return sum((differences(left.get(k), right.get(k), prefix + "/" + k) for k in sorted(set(left) | set(right))), [])
    if left == right:
        return []
    return [prefix or "/"]


def editable_view(doc):
    result = scrub(doc)
    for key in ("id", "status", "statusDetails", "friendlyName"):
        result.pop(key, None)
    return result


def approved_changes(base, wanted):
    diff = differences(editable_view(base), editable_view(wanted))
    allowed = {"/targetPublishMode", "/applicationPackages"}
    allowed.update(f"/listings/{lang}/baseListing/releaseNotes" for lang in ("pl-pl", "en-us"))
    if any(not any(p == a or p.startswith(a + "/") for a in allowed) for p in diff):
        raise Refusal("Unexpected metadata change: " + ", ".join(diff))
    return diff


def summary(state):
    return hashlib.sha256(json.dumps({"id": state.get("submissionId"), "candidate": state["candidate"],
        "proposal": editable_view(state["proposal"])}, sort_keys=True, ensure_ascii=False).encode("utf-8")).hexdigest()


def upload_zip(sas_url, package_path, state_dir):
    parsed = urllib.parse.urlparse(sas_url or "")
    if parsed.scheme != "https" or not (parsed.hostname or "").endswith(".blob.core.windows.net") or parsed.username:
        raise Refusal("Submission has no valid upload URL")
    with tempfile.TemporaryDirectory(dir=state_dir) as temp:
        archive_path = Path(temp) / "package.zip"
        with zipfile.ZipFile(archive_path, "w", compression=zipfile.ZIP_STORED) as archive:
            archive.write(package_path, Path(package_path).name)
        request = urllib.request.Request(sas_url, data=archive_path.read_bytes(), method="PUT",
            headers={"x-ms-blob-type": "BlockBlob", "Content-Type": "application/zip"})
        with urllib.request.urlopen(request, timeout=600) as response:
            if response.status not in (200, 201, 202):
                raise Refusal("Azure upload did not succeed")


def preview(api, item, state):
    print(f"Store {IDENTITY['storeId']} | {IDENTITY['identityName']} | MSIX {item['version']} | SHA-256 {item['sha256']}")
    print("Notes PL:\n" + item["notes"]["pl-pl"] + "\nNotes EN:\n" + item["notes"]["en-us"])
    if not api:
        print("Partner Center state: unavailable (no access token); no live eligibility conclusion")
        return
    app = api.app()
    pending = submission_id(app, "pendingApplicationSubmission")
    published = submission_id(app, "lastPublishedApplicationSubmission")
    print(f"Published submission: {published}; pending submission: {pending or 'none'}")
    if pending:
        live = api.submission(pending)
        print(f"Pending status: {live.get('status')}; version(s): {[p.get('version') for p in live.get('applicationPackages', [])]}")
        if not state or state.get("submissionId") != pending:
            print("Conflict: pending submission is not owned by this local workflow")
        return
    baseline = published_baseline(api, app, item)
    print("Proposed metadata paths: " + ", ".join(approved_changes(baseline, proposed(baseline, item))))
    print("targetPublishMode: Manual; submit and publication require separate decisions")


def prepare(api, item, state_path):
    state = load_state(state_path)
    app = api.app()
    pending = submission_id(app, "pendingApplicationSubmission")
    identity = {"sha256": item["sha256"], "version": item["version"], "fileName": item["fileName"]}
    if state:
        if state.get("candidate") != identity:
            raise Refusal("Local state belongs to a different candidate")
        if not state.get("submissionId"):
            raise Refusal("Create may have completed before ID was saved; inspect Partner Center and reconcile manually")
        if pending != state["submissionId"]:
            raise Refusal("Pending submission changed; refusing to overwrite unknown state")
    else:
        base = published_baseline(api, app, item)
        state = {"candidate": identity, "base": scrub(base), "proposal": scrub(proposed(base, item)), "stage": "create-intent"}
        approved_changes(base, state["proposal"])
        save_state(state_path, state)
        created = api.call("POST", "/submissions")
        sid = created.get("id")
        if not sid:
            raise Refusal("Create response had no submission ID; manual reconciliation required")
        state["submissionId"] = str(sid)
        state["createdSnapshot"] = scrub(created)
        state["stage"] = "created"
        save_state(state_path, state)
        if created.get("targetPublishMode") not in ("Manual", "Immediate", "SpecificDate"):
            raise Refusal("Create returned unknown publication mode; inspect draft")
        state["proposal"] = scrub(proposed(created, item))
        approved_changes(created, state["proposal"])
        save_state(state_path, state)
        pending = str(sid)
    if state["stage"] == "created" and state.get("createdSnapshot"):
        copy_changes = differences(editable_view(state["base"]), editable_view(state["createdSnapshot"]))
        if copy_changes:
            raise Refusal("New draft differs from published metadata before edits: " + ", ".join(copy_changes))
        state["proposal"] = scrub(proposed(state["createdSnapshot"], item))
        approved_changes(state["createdSnapshot"], state["proposal"])
        save_state(state_path, state)
    live = api.submission(state["submissionId"])
    if live.get("status") != "PendingCommit":
        raise Refusal("Submission is no longer an editable draft")
    proposal = state["proposal"]
    current = editable_view(live)
    created = editable_view(state.get("createdSnapshot", state["base"]))
    desired = editable_view(proposal)
    if current == created:
        payload = copy.deepcopy(live)
        payload["applicationPackages"] = copy.deepcopy(proposal["applicationPackages"])
        payload["listings"] = copy.deepcopy(proposal["listings"])
        payload["targetPublishMode"] = "Manual"
        approved_changes(live, payload)
        api.call("PUT", "/submissions/" + state["submissionId"], payload)
        state["stage"] = "metadata-updated"
        save_state(state_path, state)
        live = api.submission(state["submissionId"])
        current = editable_view(live)
    if current != desired:
        raise Refusal("Live draft metadata differs from saved proposal; no upload performed: " + ", ".join(differences(current, desired)))
    upload_zip(live.get("fileUploadUrl"), item["path"], Path(state_path).parent)
    state["stage"] = "uploaded"
    save_state(state_path, state)
    print(f"Draft {state['submissionId']} uploaded; NOT sent to certification. Version {item['version']}; summary SHA-256 {summary(state)}")


def submit(api, state_path, sid, version, digest):
    state = load_state(state_path)
    if not state or state.get("stage") != "uploaded":
        raise Refusal("No fully uploaded local draft")
    if (sid, version, digest) != (state.get("submissionId"), state["candidate"]["version"], summary(state)):
        raise Refusal("Explicit submission ID, version or summary digest differs")
    app = api.app()
    if submission_id(app, "pendingApplicationSubmission") != sid:
        raise Refusal("Pending submission changed")
    live = api.submission(sid)
    if live.get("status") != "PendingCommit" or live.get("targetPublishMode") != "Manual":
        raise Refusal("Draft status or manual publication mode changed")
    if editable_view(live) != editable_view(state["proposal"]):
        raise Refusal("Live metadata differs from approved summary")
    state["stage"] = "commit-intent"
    save_state(state_path, state)
    api.call("POST", "/submissions/" + sid + "/commit")
    state["stage"] = "commit-requested"
    save_state(state_path, state)
    print(f"Submission {sid} sent to certification; manual publication remains separate")


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("preview", "status", "prepare", "submit"))
    parser.add_argument("--msix")
    parser.add_argument("--sha256")
    parser.add_argument("--notes")
    parser.add_argument("--state", default=str(DEFAULT_STATE))
    parser.add_argument("--approve-create-upload", action="store_true")
    parser.add_argument("--approve-submit", action="store_true")
    parser.add_argument("--submission-id")
    parser.add_argument("--version")
    parser.add_argument("--summary-sha256")
    args = parser.parse_args(argv)
    try:
        api = APIClient(os.environ.get("FILEES_STORE_ACCESS_TOKEN")) if os.environ.get("FILEES_STORE_ACCESS_TOKEN") else None
        if args.action == "status":
            if not api:
                raise Refusal("Status requires account access")
            app = api.app()
            for kind in ("lastPublishedApplicationSubmission", "pendingApplicationSubmission"):
                sid = submission_id(app, kind)
                if sid:
                    live = api.submission(sid)
                    print(f"{kind}: {sid}; status={live.get('status')}; targetPublishMode={live.get('targetPublishMode')}; versions={[p.get('version') for p in live.get('applicationPackages', [])]}")
            return 0
        if args.action == "submit":
            if not args.approve_submit or not all((args.submission_id, args.version, args.summary_sha256)):
                raise Refusal("Submit needs separate approval plus exact ID, version and summary SHA-256")
            if not api:
                raise Refusal("Submit requires account access")
            submit(api, args.state, args.submission_id, args.version, args.summary_sha256)
            return 0
        if not all((args.msix, args.sha256, args.notes)):
            raise Refusal("MSIX, SHA-256 and PL/EN notes are required")
        item = candidate(args.msix, args.sha256, args.notes)
        if args.action == "preview":
            preview(api, item, load_state(args.state))
        else:
            if not args.approve_create_upload:
                raise Refusal("Prepare needs separate approval for draft creation and package upload")
            if not api:
                raise Refusal("Prepare requires account access")
            prepare(api, item, args.state)
        return 0
    except (Refusal, OSError, ValueError, KeyError, ET.ParseError, zipfile.BadZipFile, urllib.error.URLError) as exc:
        print("REFUSED: " + safe_error(exc), file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
