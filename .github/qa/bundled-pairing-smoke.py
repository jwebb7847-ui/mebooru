"""Exercise the bundled macOS first-party trust boundary in disposable homes."""
import contextlib
import hashlib
import http.server
import json
import os
import re
import socket
import subprocess
import sys
import tempfile
import threading
import time
import tomllib
import urllib.error
import urllib.request
from pathlib import Path

app = Path(sys.argv[1]).resolve()
binary = app / "Contents/MacOS/monbooru"
assert binary.is_file()
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def request(port, path, token=None, method="GET"):
    headers = {"Authorization": "Bearer " + token} if token else {}
    req = urllib.request.Request(
        f"http://127.0.0.1:{port}{path}", headers=headers,
        data=b"" if method == "POST" else None, method=method,
    )
    with opener.open(req, timeout=4) as response:
        return response.read(), response.headers


def wait_health(port, name, proc):
    deadline = time.monotonic() + 70
    while time.monotonic() < deadline:
        if proc.poll() is not None:
            raise AssertionError("Mebooru exited before readiness")
        try:
            body, _ = request(port, "/health")
            assert json.loads(body)["app"] == name
            return
        except OSError:
            time.sleep(0.3)
    raise AssertionError(f"Service {name} did not become ready")


def listening(port):
    try:
        with socket.create_connection(("127.0.0.1", port), timeout=0.2):
            return True
    except OSError:
        return False


@contextlib.contextmanager
def running(home, expect_loader=True):
    home.mkdir(parents=True, exist_ok=True)
    env = {k: v for k, v in os.environ.items() if not k.startswith(
        ("MEBOORU_", "MONBOORU_", "MELOADER_", "MONLOADER_")
    )}
    env["HOME"] = str(home)
    assert not listening(8455), "Unexpected pre-existing library listener"
    if expect_loader:
        assert not listening(8456), "Unexpected pre-existing loader listener"
    with (home / "smoke.log").open("a") as log:
        proc = subprocess.Popen([str(binary), "-no-browser"], env=env,
                                stdout=log, stderr=subprocess.STDOUT)
        try:
            wait_health(8455, "monbooru", proc)
            if expect_loader:
                wait_health(8456, "monloader", proc)
            yield
        finally:
            if proc.poll() is None:
                proc.terminate()
            try:
                proc.wait(timeout=45)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()
                raise AssertionError("Mebooru shutdown timed out")
            assert proc.returncode == 0, "Mebooru did not exit cleanly"
            assert not listening(8455), "Library outlived parent"
            if expect_loader:
                assert not listening(8456), "Bundled child outlived parent"


def path(home, name):
    return home / "Library/Application Support" / name / (name + ".toml")


def load(home, name):
    with path(home, name).open("rb") as f:
        return tomllib.load(f)


def paired(cfg, name):
    return [t for t in cfg.get("auth", {}).get("tokens", [])
            if t.get("paired") == name]


def pair_credentials(home):
    library, loader = load(home, "monbooru"), load(home, "monloader")
    l, m = paired(library, "monloader"), paired(loader, "monbooru")
    assert len(l) == len(m) == 1, "Expected exactly one grant on each side"
    library_secret = loader["monbooru"]["api_token"]
    loader_secret = library["monloader"]["api_token"]
    assert library_secret and loader_secret and library_secret != loader_secret
    assert l[0]["token_hash"] == hashlib.sha256(library_secret.encode()).hexdigest()
    assert m[0]["token_hash"] == hashlib.sha256(loader_secret.encode()).hexdigest()
    assert set(l[0]["scopes"]) == {"read", "write"}, "Unexpected library privilege"
    assert l[0]["peer_url"] == "http://127.0.0.1:8456"
    assert loader["monbooru"]["api_url"] == "http://127.0.0.1:8455"
    return library_secret, loader_secret, l[0]["id"], m[0]["id"]


def finish_setup_fixture(home):
    # This is a disposable fixture, not the actual Finder setup acceptance.
    p = path(home, "monbooru")
    contents = p.read_text()
    if not load(home, "monbooru").get("setup_done"):
        contents, count = re.subn(r"(?m)^setup_done\s*=\s*false\s*$",
                                  "setup_done = true", contents, count=1)
        if count == 0:
            contents = "setup_done = true\n" + contents
        p.write_text(contents)
    assert load(home, "monbooru")["setup_done"] is True


class Impostor(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = b'{"app":"monloader","status":"ok","version":"impostor"}'
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


with tempfile.TemporaryDirectory(prefix="mebooru-bundle-qa-") as scratch:
    root = Path(scratch)
    home = root / "fresh"
    with running(home):
        credentials = pair_credentials(home)
        _, headers = request(8456, "/settings")
        assert "frame-ancestors http://127.0.0.1:8455" in headers.get(
            "Content-Security-Policy", "")
    print("PASS: fresh bundled startup, matching scopes, frame policy and shutdown")

    finish_setup_fixture(home)
    with running(home):
        assert pair_credentials(home) == credentials, "Pair changed on restart"
        request(8455, "/api/v1/galleries", credentials[0])
        request(8456, "/api/v1/queue", credentials[1])
        request(8455, "/api/v1/pair/remove", credentials[0], method="POST")
        assert not paired(load(home, "monbooru"), "monloader")
        try:
            request(8455, "/api/v1/galleries", credentials[0])
        except urllib.error.HTTPError as error:
            assert error.code in (401, 403, 503)
        else:
            raise AssertionError("Revoked credential still worked")
    with running(home):
        assert not paired(load(home, "monbooru"), "monloader"), "Revoked grant returned"
    print("PASS: both APIs, restart persistence and revocation without regrant")

    # Remove only a config in this disposable directory. Existing data and
    # companion profile remain; automatic authorization must stay disabled.
    path(home, "monbooru").unlink()
    with running(home):
        assert not paired(load(home, "monbooru"), "monloader")
    print("PASS: partial profile reset did not auto-authorize")

    with http.server.ThreadingHTTPServer(("127.0.0.1", 8456), Impostor) as fake:
        thread = threading.Thread(target=fake.serve_forever, daemon=True)
        thread.start()
        try:
            conflict_home = root / "conflict"
            with running(conflict_home, expect_loader=False):
                assert not paired(load(conflict_home, "monbooru"), "monloader")
                assert not paired(load(conflict_home, "monloader"), "monbooru")
            body, _ = request(8456, "/health")
            assert json.loads(body)["version"] == "impostor"
        finally:
            fake.shutdown()
            thread.join(timeout=5)
    print("PASS: occupied port / impersonated health response refused; external listener survived")
