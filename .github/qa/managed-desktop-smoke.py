"""Exercise offline first run and Quit in the signed Apple Silicon app."""
import http.client
import os
import re
import socket
import subprocess
import sys
import tempfile
import time
import urllib.parse
from pathlib import Path

app = Path(sys.argv[1]).resolve()
binary = app / "Contents/MacOS/monbooru"

with socket.socket() as listener:
    listener.bind(("127.0.0.1", 0))
    port = listener.getsockname()[1]


def request(path, expected, form=None):
    conn = http.client.HTTPConnection("127.0.0.1", port, timeout=5)
    try:
        if form is None:
            conn.request("GET", path)
        else:
            conn.request("POST", path, urllib.parse.urlencode(form),
                         {"Content-Type": "application/x-www-form-urlencoded"})
        res = conn.getresponse()
        body = res.read().decode()
        assert res.status == expected, (path, res.status, body[:400])
        return body
    finally:
        conn.close()


def csrf(page):
    return re.search(r'name="_csrf" value="([^"]+)"', page)[1]


with tempfile.TemporaryDirectory(prefix="mebooru-desktop-") as scratch:
    root = Path(scratch)
    gallery = root / "gallery"
    gallery.mkdir()
    config = root / "monbooru.toml"
    env = {k: v for k, v in os.environ.items() if not k.startswith(
        ("MEBOORU_", "MONBOORU_", "MELOADER_", "MONLOADER_")
    )}
    env.update(PATH="/usr/bin:/bin", MEBOORU_SERVER_BIND_ADDRESS=f"127.0.0.1:{port}")
    log_path = root / "app.log"
    with log_path.open("w") as log:
        proc = subprocess.Popen([binary, "-no-browser", "-config", config],
                                env=env, cwd=root, stdout=log, stderr=subprocess.STDOUT)
        try:
            deadline = time.monotonic() + 30
            while True:
                try:
                    wizard = request("/setup", 200)
                    break
                except OSError:
                    if proc.poll() is not None or time.monotonic() >= deadline:
                        raise
                    time.sleep(0.2)
            request("/setup", 303, {"_csrf": csrf(wizard), "gallery_path": gallery, "reach": "local"})
            settings = request("/settings", 200)
            assert "gallery-dl 1.32.19 · Ready" in settings
            assert "Acquisition is offline." in settings
            assert "managed_acquisition = true" in config.read_text()
            request("/settings/quit", 200, {"_csrf": csrf(settings)})
            assert proc.wait(timeout=25) == 0
        except BaseException:
            print(log_path.read_text(errors="replace"))
            raise
        finally:
            if proc.poll() is None:
                proc.terminate()
                proc.wait(timeout=25)

print("First-run setup, bundled gallery-dl, offline default and Quit passed.")
