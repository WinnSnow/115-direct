#!/usr/bin/env python3
"""Small read-only CMS/Jellyfin acceptance runner; secrets stay outside the repo."""
import argparse
import base64
import json
import os
import hashlib
import time
import subprocess
from pathlib import Path
import urllib.parse
import urllib.request

from cryptography.hazmat.primitives.ciphers.aead import AESGCM

ROOT = Path(os.environ.get("CMS_ACCEPTANCE_DIR", "/var/tmp/115-direct-cms-acceptance"))
BACKEND = os.environ.get("JELLYFIN_URL", "http://jellyfin.example.test:8091")
CMS_URL = os.environ.get("CMS_URL", "http://cms.example.test:9527")
GATEWAY_URL = os.environ.get("GATEWAY_URL", "http://127.0.0.1:29096")
REPORT_URL = os.environ.get("REPORT_URL", "http://127.0.0.1:29527")


def secrets():
    raw = (ROOT / "session.enc").read_bytes()
    data = base64.urlsafe_b64decode(raw + b"=" * (-len(raw) % 4))
    return json.loads(AESGCM((ROOT / "master.key").read_bytes()).decrypt(data[:12], data[12:], None))


def save_private(name, value):
    path = ROOT / name
    with open(path, "w", encoding="utf-8") as file:
        os.chmod(path, 0o600)
        json.dump(value, file, ensure_ascii=False, indent=2)


def api(path, base=BACKEND, body=None):
    auth = secrets()
    headers = {"X-Emby-Token": auth["token"], "Accept": "application/json", "User-Agent": "115-direct-acceptance/1.0"}
    data = None
    if body is not None:
        data = json.dumps(body).encode()
        headers["Content-Type"] = "application/json"
    request = urllib.request.Request(base + path, data=data, headers=headers)
    with urllib.request.urlopen(request, timeout=30) as response:
        return json.load(response)


def catalog():
    uid = secrets()["user_id"]
    catalog = {}
    for kind in ("Movie", "Episode"):
        query = urllib.parse.urlencode({"Recursive": "true", "IncludeItemTypes": kind, "Fields": "Path,MediaSources,ProviderIds,Overview,Genres", "Limit": 30, "SortBy": "SortName", "SortOrder": "Ascending"})
        result = api(f"/Users/{uid}/Items?{query}")
        catalog[kind] = result["Items"]
        print(kind, "visible_total", result.get("TotalRecordCount"), "read", len(result["Items"]))
        for item in result["Items"][:12]:
            streams = [stream for source in item.get("MediaSources", []) for stream in source.get("MediaStreams", [])]
            print(json.dumps({"id": item["Id"], "name": item["Name"], "year": item.get("ProductionYear"), "season": item.get("ParentIndexNumber"), "episode": item.get("IndexNumber"), "codecs": [s.get("Codec") for s in streams if s.get("Type") in ("Video", "Audio")], "source_hosts": [urllib.parse.urlsplit(s.get("Path", "")).hostname for s in item.get("MediaSources", [])]}, ensure_ascii=False))
    save_private("catalog.json", catalog)


def select():
    catalog = json.loads((ROOT / "catalog.json").read_text())
    samples = []
    uid = secrets()["user_id"]
    for kind in ("Movie", "Episode"):
        found = None
        for item in catalog[kind]:
            for source in item.get("MediaSources", []):
                streams = source.get("MediaStreams", [])
                if any(s.get("Type") == "Video" and s.get("Codec") == "h264" for s in streams) and any(s.get("Type") == "Audio" and s.get("Codec") == "aac" for s in streams):
                    found = (item, source)
                    break
            if found:
                break
        if not found:
            raise RuntimeError("No H264/AAC sample in bounded catalog")
        item, source = found
        details = api(f"/Users/{uid}/Items/{item['Id']}")
        playback = api(f"/Items/{item['Id']}/PlaybackInfo?UserId={uid}")
        samples.append({"kind": kind, "item_id": item["Id"], "name": item["Name"], "source_id": source["Id"], "source": source, "details": details, "playback": playback})
        print(json.dumps({"selected": kind, "name": item["Name"], "series": item.get("SeriesName"), "season": item.get("ParentIndexNumber"), "episode": item.get("IndexNumber"), "sources": len(playback.get("MediaSources", []))}, ensure_ascii=False))
    save_private("samples.json", samples)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def redirect(location, ua):
    opener = urllib.request.build_opener(NoRedirect())
    request = urllib.request.Request(location, headers={"User-Agent": ua})
    try:
        with opener.open(request, timeout=30) as response:
            return response.status, response.headers.get("Location"), response.headers.get("X-CMS-Link-Mode")
    except urllib.error.HTTPError as error:
        return error.code, error.headers.get("Location"), error.headers.get("X-CMS-Link-Mode")


def probe(location, ua):
    request = urllib.request.Request(location, headers={"User-Agent": ua, "Range": "bytes=0-65535", "Accept-Encoding": "identity"})
    with urllib.request.urlopen(request, timeout=30) as response:
        data = response.read(65536)
        return {"status": response.status, "content_range": response.headers.get("Content-Range"), "content_type": response.headers.get("Content-Type"), "bytes": len(data), "sha256": hashlib.sha256(data).hexdigest()}


def baseline():
    uid = secrets()["user_id"]
    ua = "115-direct-acceptance/1.0"
    results = []
    for sample in json.loads((ROOT / "samples.json").read_text()):
        old = urllib.parse.urlsplit(sample["source"]["Path"])
        cms_base = urllib.parse.urlsplit(CMS_URL)
        cms_url = urllib.parse.urlunsplit((cms_base.scheme, cms_base.netloc, old.path, urllib.parse.quote(old.query, safe="/?=&%+"), ""))
        status, location, _ = redirect(cms_url, ua)
        result = {"kind": sample["kind"], "name": sample["name"], "cms_status": status}
        if status != 302 or not location:
            result["error"] = "CMS baseline did not return 302"
            results.append(result)
            continue
        cms_probe = probe(location, ua)
        rewritten = api(f"/Items/{sample['item_id']}/PlaybackInfo?UserId={uid}", base=GATEWAY_URL)
        selected = next(s for s in rewritten["MediaSources"] if s["Id"] == sample["source_id"])
        path = selected["Path"]
        if not path.startswith(GATEWAY_URL + "/cms/play/"):
            raise RuntimeError("selected playback source was not rewritten")
        gate_status, gate_location, mode = redirect(path, ua)
        result.update({"gateway_status": gate_status, "mode": mode, "rewritten": True, "transcoding": selected.get("SupportsTranscoding")})
        if gate_status == 302 and gate_location:
            gate_probe = probe(gate_location, ua)
            result.update({"cms_probe": cms_probe, "gateway_probe": gate_probe, "same_media_bytes": cms_probe["sha256"] == gate_probe["sha256"]})
            before = json.load(urllib.request.urlopen(REPORT_URL + "/healthz"))["upstream_requests"]
            cached_status, _, _ = redirect(path, ua)
            after = json.load(urllib.request.urlopen(REPORT_URL + "/healthz"))["upstream_requests"]
            result.update({"cache_status": cached_status, "cache_no_upstream_request": before == after})
        print(json.dumps(result, ensure_ascii=False))
        results.append(result)
        time.sleep(3)
    save_private("baseline.json", results)


def independent():
    """No CMS request: compare independent links with the saved first-round hash."""
    uid = secrets()["user_id"]
    ua = "115-direct-acceptance/1.0"
    references = json.loads((ROOT / "reference-baseline.json").read_text())
    results = []
    health_url = REPORT_URL + "/healthz"
    initial = json.load(urllib.request.urlopen(health_url))
    if initial.get("mode") != "independent-115" or initial.get("pan_login_enabled") is not False:
        raise RuntimeError("Independent reuse-only gateway required")
    for sample in json.loads((ROOT / "samples.json").read_text()):
        old = urllib.parse.urlsplit(sample["source"]["Path"])
        legacy = urllib.parse.urlunsplit(("http", "127.0.0.1:29528", old.path, urllib.parse.quote(old.query, safe="/?=&%+"), ""))
        legacy_status, legacy_location, legacy_mode = redirect(legacy, ua)
        result = {"name":sample["name"], "kind":sample["kind"], "legacy_status":legacy_status, "legacy_mode":legacy_mode}
        reference = next(x for x in references if x["kind"] == sample["kind"] and x["name"] == sample["name"])
        if legacy_status == 302 and legacy_location:
            legacy_probe = probe(legacy_location, ua)
            result.update({"legacy_probe":legacy_probe, "same_reference_bytes":legacy_probe["sha256"] == reference["cms_probe"]["sha256"], "same_reference_size":legacy_probe["content_range"] == reference["cms_probe"]["content_range"]})
        rewritten = api(f"/Items/{sample['item_id']}/PlaybackInfo?UserId={uid}", base=GATEWAY_URL)
        selected = next(s for s in rewritten["MediaSources"] if s["Id"] == sample["source_id"])
        if not selected["Path"].startswith(GATEWAY_URL + "/cms/play/"):
            raise RuntimeError("Selected source was not rewritten")
        status, location, mode = redirect(selected["Path"], ua)
        result.update({"gateway_status":status, "mode":mode, "transcoding":selected.get("SupportsTranscoding")})
        if status == 302 and location:
            current = probe(location, ua)
            result.update({"gateway_probe":current, "same_legacy_bytes":current["sha256"] == result.get("legacy_probe",{}).get("sha256")})
            before = json.load(urllib.request.urlopen(health_url))["pan_read_requests"]
            cached_status, _, _ = redirect(selected["Path"], ua)
            after = json.load(urllib.request.urlopen(health_url))["pan_read_requests"]
            result.update({"cache_status":cached_status, "cache_no_pan_request":before == after})
        print(json.dumps(result, ensure_ascii=False))
        results.append(result)
    before = json.load(urllib.request.urlopen(health_url))["pan_read_requests"]
    rejected, _, _ = redirect("http://127.0.0.1:29528/d/otherpick123.mkv", ua)
    after = json.load(urllib.request.urlopen(health_url))["pan_read_requests"]
    protections = {"outside_scope_status":rejected, "outside_scope_no_pan_request":before == after, "health":json.load(urllib.request.urlopen(health_url))}
    print(json.dumps(protections))
    save_private("independent.json", {"results":results, "protections":protections})
    if not all(x.get("same_reference_bytes") and x.get("same_reference_size") and x.get("same_legacy_bytes") and x.get("cache_no_pan_request") and x.get("mode") == "independent-115" for x in results):
        raise RuntimeError("Independent link acceptance did not pass")


def verify():
    uid = secrets()["user_id"]
    immutable = ["Name", "OriginalTitle", "Overview", "ProductionYear", "ProviderIds", "Path", "IndexNumber", "ParentIndexNumber", "SeriesName"]
    results = []
    for sample in json.loads((ROOT / "samples.json").read_text()):
        after = api(f"/Users/{uid}/Items/{sample['item_id']}")
        before = sample["details"]
        fields_equal = all(before.get(k) == after.get(k) for k in immutable)
        paths_equal = [(s.get("Id"), s.get("Path")) for s in before.get("MediaSources", [])] == [(s.get("Id"), s.get("Path")) for s in after.get("MediaSources", [])]
        result = {"name": sample["name"], "metadata_unchanged": fields_equal, "source_paths_unchanged": paths_equal,
                  "inherited": {k: before.get(k) for k in ["Name", "ProductionYear", "ProviderIds", "SeriesName", "ParentIndexNumber", "IndexNumber"]},
                  "nfo_and_image_files": "pending sample copies; API metadata is not original-file import"}
        results.append(result)
        print(json.dumps(result, ensure_ascii=False))
    status, _, _ = redirect(GATEWAY_URL + "/cms/play/fixturepick123.mkv", "Acceptance/1")
    results.append({"unauthorized_playback_status": status})
    req = urllib.request.Request(GATEWAY_URL + "/Library/Refresh", data=b"{}", headers={"X-Emby-Token": secrets()["token"], "Content-Type": "application/json"})
    try:
        urllib.request.urlopen(req, timeout=10)
        blocked = False
    except urllib.error.HTTPError as error:
        blocked = error.code == 403
    results.append({"remote_write_blocked": blocked})
    print(json.dumps(results[-2:], ensure_ascii=False))
    save_private("verification.json", results)


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("mode", choices=["catalog", "select", "baseline", "independent", "browser", "verify"])
    args = parser.parse_args()
    if args.mode == "catalog":
        catalog()
    elif args.mode == "select":
        select()
    elif args.mode == "baseline":
        baseline()
    elif args.mode == "independent":
        independent()
    elif args.mode == "browser":
        private = secrets()
        private["username"] = "test-115-direct"
        private["samples"] = json.loads((ROOT / "samples.json").read_text())
        subprocess.run(["node", "web/tests/cms-acceptance.mjs"], input=json.dumps(private).encode(), check=True)
    elif args.mode == "verify":
        verify()
