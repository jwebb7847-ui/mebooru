"""Build real sibling binaries and exercise the private no-acquisition probe."""
import hashlib
import os
import shutil
import sys
import subprocess
import tempfile
from pathlib import Path

workspace = Path.cwd()
env = dict(os.environ, CGO_ENABLED="0", GOTOOLCHAIN="local", GOWORK="off")
suffix = ".exe" if os.name == "nt" else ""
def verify_pinned_runtime(library, directory):
    # Opt-in development gate only. Acquires an isolated copy of the
    # already qualified PyPI runtime; no media requests or live-site tests.
    pins = dict(line.split("=", 1) for line in
                (workspace / "meloader/packaging/pins.env").read_text().splitlines()
                if line and not line.startswith("#") and "=" in line)
    version = pins["GDL_VERSION"]
    with tempfile.TemporaryDirectory(prefix="mebooru-pinned-runtime-") as scratch:
        venv = Path(scratch) / "venv"
        subprocess.run([sys.executable, "-m", "venv", str(venv)], check=True,
                       timeout=120)
        bin_dir = venv / ("Scripts" if os.name == "nt" else "bin")
        python = bin_dir / ("python.exe" if os.name == "nt" else "python")
        source = bin_dir / ("gallery-dl" + suffix)
        subprocess.run([str(python), "-m", "pip", "install", "--disable-pip-version-check",
                        "gallery-dl==" + version], check=True, timeout=180)
        assert subprocess.check_output([str(source), "--version"], text=True,
                                       timeout=20).strip() == version
        candidate = directory / source.name
        shutil.copy2(source, candidate)
        marker = directory / "gallery-dl.probe.sha256"
        # Fixture identity only: this hash does not authenticate a PyPI
        # release, interpreter or dependency tree for production.
        digest = hashlib.sha256(candidate.read_bytes()).hexdigest()
        marker.write_text(digest + "\n", encoding="ascii", newline="\n")
        try:
            result = subprocess.run([str(library), "internal-managed-runtime-version-probe-v1"],
                                    stdin=subprocess.DEVNULL, capture_output=True,
                                    cwd=directory, env=env, timeout=40, text=True)
            print(result.stdout, result.stderr, sep="")
            assert result.returncode == 0, result.returncode
            assert "PASS: contained gallery-dl version probe: " + version in result.stdout
            broker = subprocess.run([str(library), "internal-managed-runtime-broker-probe-v1"],
                                    stdin=subprocess.DEVNULL, capture_output=True,
                                    cwd=directory, env=env, timeout=80, text=True)
            print(broker.stdout, broker.stderr, sep="")
            assert broker.returncode == 0, broker.returncode
            assert "PASS: brokered gallery-dl version probe: " + version in broker.stdout
            marker.write_text("0" * 64 + "\n", encoding="ascii", newline="\n")
            refused = subprocess.run([str(library), "internal-managed-runtime-version-probe-v1"],
                                     stdin=subprocess.DEVNULL, capture_output=True,
                                     cwd=directory, env=env, timeout=10)
            assert refused.returncode != 0, refused
            assert b"PASS: contained gallery-dl version probe" not in refused.stdout
            refused_broker = subprocess.run([str(library), "internal-managed-runtime-broker-probe-v1"],
                                            stdin=subprocess.DEVNULL, capture_output=True,
                                            cwd=directory, env=env, timeout=10)
            assert refused_broker.returncode != 0, refused_broker
            assert b"PASS: brokered gallery-dl version probe" not in refused_broker.stdout
            # Keep the exact existing positive/negative filter and media
            # boundary fixtures; never run live-source acquisition here.
            if os.name != "nt":
                strict_env = dict(env, MELOADER_STRICT_TEST_GDL=str(source))
                subprocess.run(["go", "test", "-count=1", "-timeout=3m", "-v",
                                "-run", "^TestStrictGalleryDLPreflight$", "./internal/gdl"],
                               cwd=workspace / "meloader", env=strict_env, check=True, timeout=240)
                subprocess.run(["go", "test", "-count=1", "-timeout=3m", "-v",
                                "-run", "^TestStrictBlacklistGalleryDLMediaBoundary$", "./internal/pipeline"],
                               cwd=workspace / "meloader", env=strict_env, check=True, timeout=240)
        finally:
            marker.unlink(missing_ok=True)
            candidate.unlink(missing_ok=True)

with tempfile.TemporaryDirectory(prefix="mebooru-probe-build-") as scratch:
    directory = Path(scratch)
    library = directory / ("monbooru" + suffix)
    loader = directory / ("monloader" + suffix)
    for repo, name, binary in [(workspace, "monbooru", library),
                                (workspace / "meloader", "monloader", loader)]:
        subprocess.run(["go", "build", "-o", str(binary), "./cmd/" + name],
                       cwd=repo, env=env, check=True)
    # No inherited activation or protocol handles: reject, never fall
    # through into normal app startup. stdin is explicitly EOF.
    refused = subprocess.run([str(loader), "internal-managed-worker-probe-v1"],
                             stdin=subprocess.DEVNULL, capture_output=True,
                             cwd=directory, env=env, timeout=10)
    assert refused.returncode != 0, refused
    result = subprocess.run([str(library), "internal-managed-probe-v1"],
                            stdin=subprocess.DEVNULL, capture_output=True,
                            cwd=directory, env=env, timeout=90, text=True)
    print(result.stdout, result.stderr, sep="")
    assert result.returncode == 0, result.returncode
    assert "PASS: managed executable probe (no acquisition)" in result.stdout
    crash = subprocess.run([str(library), "internal-managed-crash-probe-v1"],
                           stdin=subprocess.DEVNULL, capture_output=True,
                           cwd=directory, env=env, timeout=100, text=True)
    print(crash.stdout, crash.stderr, sep="")
    assert crash.returncode == 0, crash.returncode
    assert "PASS: managed executable crash recovery (active operation)" in crash.stdout
    refused_crash = subprocess.run([str(loader), "internal-managed-worker-loss-probe-v1"],
                                   stdin=subprocess.DEVNULL, capture_output=True,
                                   cwd=directory, env=env, timeout=10)
    assert refused_crash.returncode != 0, refused_crash
    worker_crash = subprocess.run([str(library), "internal-managed-worker-crash-probe-v1"],
                                  stdin=subprocess.DEVNULL, capture_output=True,
                                  cwd=directory, env=env, timeout=100, text=True)
    print(worker_crash.stdout, worker_crash.stderr, sep="")
    assert worker_crash.returncode == 0, worker_crash.returncode
    assert "PASS: managed executable worker loss recovery (active operation)" in worker_crash.stdout
    if os.environ.get("MEBOORU_PROBE_PINNED_RUNTIME") == "1":
        verify_pinned_runtime(library, directory)
    assert sorted(p.name for p in directory.iterdir()) == sorted([library.name, loader.name])
