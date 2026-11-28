import os
import sys
from pathlib import Path

runtime = Path(__file__).resolve().parent
sys.path.insert(0, str(runtime / "packages"))
if wheel := os.environ.get("MEBOORU_RUNTIME_WHEEL"):
    sys.path.insert(0, wheel)

from gallery_dl import main

main()
