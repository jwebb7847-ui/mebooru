"""Build real sibling binaries and exercise the private no-acquisition probe."""
import hashlib
import os
import platform
import shutil
import sys
import subprocess
import tempfile
import urllib.request
from pathlib import Path

workspace = Path.cwd()
env = dict(os.environ, CGO_ENABLED="0", GOTOOLCHAIN="local", GOWORK="off")
suffix = ".exe" if os.name == "nt" else ""
def verify_transfer_cancel(library, directory):
    result = subprocess.run([str(library), "internal-managed-runtime-transfer-cancel-probe-v1"],
                            stdin=subprocess.DEVNULL, capture_output=True,
                            cwd=directory, env=env, timeout=60, text=True)
    print(result.stdout, result.stderr, sep="")
    assert result.returncode == 0, result.returncode
    assert "PASS: contained gallery-dl active transfer cancellation" in result.stdout
    drain = subprocess.run([str(library), "internal-managed-runtime-drain-probe-v1"],
                           stdin=subprocess.DEVNULL, capture_output=True,
                           cwd=directory, env=env, timeout=80, text=True)
    print(drain.stdout, drain.stderr, sep="")
    assert drain.returncode == 0, drain.returncode
    assert "PASS: brokered gallery-dl transfer offline drain" in drain.stdout


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
            baseline = subprocess.check_output([str(candidate), "--config-ignore", "--list-extractors"],
                                               cwd=directory, env=env, timeout=30)
            assert baseline
            assert ("PASS: brokered gallery-dl catalog stream: " + str(len(baseline)) + " " +
                    hashlib.sha256(baseline).hexdigest()) in broker.stdout
            verify_transfer_cancel(library, directory)
            # Actual pinned extractors, synthetic source responses, no sockets.
            metadata_env = dict(env, MEBOORU_METADATA_TEST_PYTHON=str(python))
            subprocess.run(["go", "test", "-count=1", "-timeout=3m", "-v",
                            "-run", "^Test(MetadataOperation|StrictTransferOperation)PinnedRuntime$", "./processguard"],
                           cwd=workspace / "shared", env=metadata_env, check=True, timeout=240)
            subprocess.run(["go", "test", "-count=1", "-timeout=3m", "-v",
                            "-run", "^TestManagedStrictTransferPinnedRuntime$", "./internal/pipeline"],
                           cwd=workspace / "meloader", env=metadata_env, check=True, timeout=240)
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

def verify_standalone_runtime(library, directory):
    # Separate opt-in hosted gate. Use the existing release-asset pins,
    # not the fixture-generated digest as the source of release trust.
    # The actual runtime command is still local-only.
    if platform.machine().lower() not in ("amd64", "x86_64"):
        raise RuntimeError("standalone runtime probe requires pinned x86-64 asset")
    pins = dict(line.split("=", 1) for line in
                (workspace / "meloader/packaging/pins.env").read_text().splitlines()
                if line and not line.startswith("#") and "=" in line)
    version = pins["GDL_VERSION"]
    if os.name == "nt":
        asset, expected = "gallery-dl.exe", pins["GDL_SHA256_WINDOWS_AMD64"]
    elif sys.platform == "linux":
        asset, expected = "gallery-dl.bin", pins["GDL_SHA256_LINUX_AMD64"]
    else:
        raise RuntimeError("no pinned standalone runtime for this OS")
    url = "https://codeberg.org/mikf/gallery-dl/releases/download/v" + version + "/" + asset
    candidate = directory / ("gallery-dl" + suffix)
    marker = directory / "gallery-dl.probe.sha256"
    try:
        digest = hashlib.sha256()
        total = 0
        request = urllib.request.Request(url, headers={"User-Agent": "Mebooru-QA"})
        with urllib.request.urlopen(request, timeout=30) as response, candidate.open("xb") as out:
            while True:
                chunk = response.read(1 << 20)
                if not chunk:
                    break
                total += len(chunk)
                if total > 128 << 20:
                    raise RuntimeError("standalone asset exceeds bounded size")
                digest.update(chunk)
                out.write(chunk)
        if digest.hexdigest() != expected:
            raise RuntimeError("standalone asset does not match repository pin")
        candidate.chmod(0o755)
        marker.write_text(expected + "\n", encoding="ascii", newline="\n")
        for mode, label in [
            ("internal-managed-runtime-version-probe-v1", "contained"),
            ("internal-managed-runtime-broker-probe-v1", "brokered"),
        ]:
            result = subprocess.run([str(library), mode], stdin=subprocess.DEVNULL,
                                    capture_output=True, cwd=directory, env=env,
                                    timeout=80, text=True)
            print(result.stdout, result.stderr, sep="")
            assert result.returncode == 0, result.returncode
            assert "PASS: " + label + " gallery-dl version probe: " + version in result.stdout
            if label == "brokered":
                baseline = subprocess.check_output([str(candidate), "--config-ignore", "--list-extractors"],
                                                   cwd=directory, env=env, timeout=30)
                assert baseline
                assert ("PASS: brokered gallery-dl catalog stream: " + str(len(baseline)) + " " +
                        hashlib.sha256(baseline).hexdigest()) in result.stdout
        verify_transfer_cancel(library, directory)
        print("PASS: pinned standalone gallery-dl version through custody:", version)
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
    if os.environ.get("MEBOORU_PROBE_STANDALONE_RUNTIME") == "1":
        verify_standalone_runtime(library, directory)
    assert sorted(p.name for p in directory.iterdir()) == sorted([library.name, loader.name])
