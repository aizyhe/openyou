#!/usr/bin/env bash
# Fast iteration: merge web/ sources only — no Go/APK build needed.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
python3 "$SCRIPT_DIR/build.py"
echo "→ Rebuilt index.html. Refresh the WebView to see changes."
