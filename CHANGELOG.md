# Changelog

## v1.6.0 (2026-07-15)
- **Microsoft Checker** — Full auth flow: `legacyDirectLogin` → `hardcodedLogin` → `msalLogin` with Xbox Live → XSTS → Minecraft token chain validation. Valid MS accounts saved to `VALIDMS/` folder.
- **Claymorphic UI** — Redesigned frontend with dark claymorphism: rounded cards, soft gradients, inset highlights, Poppins font.
- **Profiles directory** — Hit output now saves to `C:\Users\<user>\Documents\profiles\` instead of app-local `results/`.
- **First-startup overlay** — Name entry screen on first run; saved to `profiles/user.json`. Dashboard shows "Welcome back, {name}".
- **Working status card** — Live dashboard card showing filter count, total runs, total hits with pulsing status dot.
- **ETA timer** — Real-time estimated time remaining shown during sorting runs.
- **Session logging** — `retard-sorter.log` in app directory logs startup/shutdown, run progress every 1000 files, and panics.

## v1.5.2 (2026-07-14)
- Fix crash on large file parsing (64KB line limit fully resolved)
- Improve proxy validation timeout handling
- Remove ZIP/RAR import and proxy upload functionality
- Performance improvements for high file count runs

## v1.5.1 (2026-07-13)
- Fix cookie parser 64KB line limit
- Increase proxy validation timeout
- macOS .app bundles with `Info.plist` and `make-dmg.sh` build script
- Fix `openBrowser` for macOS and Linux

## v1.5.0 (2026-07-12)
- Proxy import and validation (HTTP, SOCKS4, SOCKS5)
- Dock UI magnification effect on hover
- Cookie scanner fix for malformed Netscape files
- Initial SSE-based progress streaming
- Server-Sent Events for real-time frontend updates

## v1.0.0 (2026-07-10)
- Initial release
- Netscape cookie file parsing
- Domain-based filtering with configurable target list
- Web GUI with dark theme
- Run history and summary tracking
- Go single-binary deployment
