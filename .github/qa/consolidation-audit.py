"""Verify one common source tree and independent module selection.

Run from a Git checkout; no media, profiles, live sites or release publishing.
"""
import json
import os
from pathlib import Path
import subprocess

root = Path(__file__).resolve().parents[2]
env = dict(os.environ, GOWORK="off", GOTOOLCHAIN="local")
tracked = set(
    subprocess.check_output(["git", "ls-files", "-z"], cwd=root)
    .decode("utf-8").rstrip("\0").split("\0")
)
legacy = ("internal/shared/", "meloader/internal/shared/")
old_imports = (
    "github.com/monbooru/monbooru/internal/shared/",
    "github.com/monbooru/monloader/internal/shared/",
)
module = "github.com/jwebb7847-ui/mebooru/shared"
required = {"apitoken", "bundlepair", "contract", "desktop", "fsx",
            "logx", "pairing", "session", "webui"}
errors = []
for name in sorted(tracked):
    if name.startswith(legacy):
        errors.append("legacy common tree: " + name)
    if name.endswith(".go"):
        text = (root / name).read_text(encoding="utf-8")
        if any(old in text for old in old_imports):
            errors.append("legacy common import: " + name)
packages = {p.split("/")[1] for p in tracked
            if p.startswith("shared/") and p.count("/") >= 2 and p.endswith(".go")}
for name in sorted(required - packages):
    errors.append("missing common package: " + name)
for path in ("shared/go.mod", "shared/go.sum",
             "shared/desktop/tray_linux_bridge.c",
             "shared/webui/static/base.css", "shared/webui/static/base.js",
             "shared/webui/static/htmx.min.js", "shared/webui/templates/head.html",
             "shared/webui/static/jetbrains-mono-OFL.txt",
             "shared/webui/static/wordmark-OFL.txt"):
    if path not in tracked:
        errors.append("missing shared build input: " + path)
if errors:
    raise SystemExit("\n".join(errors))
for directory in (root, root / "meloader"):
    info = json.loads(subprocess.check_output(
        ["go", "list", "-mod=readonly", "-m", "-json", module],
        cwd=directory, env=env, text=True,
    ))
    replacement = info.get("Replace") or {}
    if not replacement.get("Dir") or Path(replacement["Dir"]).resolve() != root / "shared":
        raise SystemExit(f"{directory.name}: common module is not the repository's shared/ directory")
    print(f"{directory.name}: common module resolves to shared/")
for directory in (root, root / "meloader", root / "shared"):
    subprocess.run(["go", "list", "-mod=readonly", "-m", "all"],
                   cwd=directory, env=env, check=True, stdout=subprocess.DEVNULL)
subprocess.run(["git", "diff", "--exit-code", "HEAD", "--",
                "go.mod", "go.sum", "meloader/go.mod", "meloader/go.sum",
                "shared/go.mod", "shared/go.sum"], cwd=root, check=True)
print(f"PASS: {len(required)} required common packages, no legacy source/imports, local module resolution and read-only dependency metadata")
