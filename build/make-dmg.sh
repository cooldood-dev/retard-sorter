#!/bin/bash
# Run this on macOS to create a .dmg from the .app bundle
# Requires create-dmg (brew install create-dmg) or uses hdiutil directly

set -e
DIR="$(cd "$(dirname "$0")" && pwd)"
BUILD="$DIR/darwin"
APP="$BUILD/Retard Sorter.app"
DMG="$BUILD/Retard-Sorter-1.5.1-arm64.dmg"

if [ ! -d "$APP" ]; then
  echo "Error: .app bundle not found at $APP"
  exit 1
fi

# Remove quarantine attribute so macOS doesn't block it
xattr -d com.apple.quarantine "$APP" 2>/dev/null || true

# Create DMG using create-dmg if available, else fallback to hdiutil
if command -v create-dmg &>/dev/null; then
  create-dmg \
    --volname "Retard Sorter" \
    --window-pos 200 120 \
    --window-size 600 400 \
    --icon-size 100 \
    --app-drop-link 380 185 \
    --icon "Retard Sorter.app" 200 185 \
    "$DMG" \
    "$APP"
  echo "Created $DMG via create-dmg"
else
  # Fallback: basic read-write DMG using hdiutil
  STAGING=$(mktemp -d)
  cp -R "$APP" "$STAGING/"
  ln -s /Applications "$STAGING/Applications"
  hdiutil create -volname "Retard Sorter" -srcfolder "$STAGING" -ov -format UDZO "$DMG"
  rm -rf "$STAGING"
  echo "Created $DMG via hdiutil"
fi
