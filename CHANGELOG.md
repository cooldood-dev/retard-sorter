# Changelog

## v1.9.0 (2026-08-01)
- **Convertor** — New Convertor page that mass-converts `email:pass` accounts into per-account Microsoft cookie files. Each account is logged in through a headless Chromium/Edge browser (`go-rod`) on `login.live.com`, session cookies are validated against the OAuth userinfo endpoint, and saved as Netscape cookie files named `retard_<random>.txt`.
- **Worker pool** — 1-20 parallel headless browsers, default 5. With a proxy file uploaded, each worker runs its own browser instance so proxies are rotated across the pool.
- **Retry logic** — Transient failures (timeouts, network/DOM issues, session extraction failures) are retried with backoff; definitive results (MFA, bad credentials, locked, captcha) are final.
- **Reporting** — Every run writes `conversion_log.csv` (email, password, status, message, retries) and `failed_credentials.txt`; results land in `results/convert_run_<timestamp>/`.
- **Dry run** — Parse-only mode that counts valid/duplicate/unparsed lines without launching a browser.
- **Browser safety** — Per-account incognito contexts, randomized user agents, human-like delays (1-3s between actions, 40-120ms between keystrokes), anti-automation flags.

## v1.8.0 (2026-08-01)
- **Account Checker** — New Checker page embedding the r slur sorter checker (faithful port of `rslursorter/checker.py`: OAuth → XBL → XSTS → Minecraft auth chain, 5-proxy retry logic, identical messages and decision rules).
- **Two-step pipeline** — Step 1 sorts the input account folder (filters invalid formats, removes duplicates, sorts) into `results/hits/`. Step 2 validates the sorted accounts with the embedded checker.
- **Toggle options** — Checkbox toggles before processing: `[ ] Microsoft Validator` (valid MS accounts → `results/valid_microsoft/`) and `[ ] Minecraft+MS Validator` (valid MS + Minecraft → `results/valid_minecraft/`). Minecraft toggle implies Microsoft validation.
- **Per-run folder structure** — `results/<run>/hits`, `results/<run>/valid_microsoft` (when validation runs), `results/<run>/valid_minecraft` (only when Minecraft+MS enabled). SSE progress + summary.json per run.

## v1.7.1 (2026-07-31)
- **100% coverage sorting** — Cookie mode now processes every file in the source folder (any depth, any name), removing the filtered collection step that could skip data. Everything is scanned; no files are left unsorted.
- **`retard_` output prefix** — All sorted output files are now named with a `retard_` prefix: cookie hits as `retard_<source>.txt`, ULP hits as `retard_hits.txt`, and skipped lines as `retard_skipped.txt`.

## v1.7.0 (2026-07-31)
- **ULP sorting mode** — New sort category for lines of `URL : Email : Pass` (one ULP per line in txt files). The URL host is matched against the filter domains, and matched lines are written as `email:pass` into a single `hits.txt` in the run's hits folder. Pick the mode on the Run page (Cookies / ULP).

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
