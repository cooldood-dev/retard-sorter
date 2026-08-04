// File: internal/auth/revalidate.go
//
// OFFLINE VS INVALID — read before editing.
//
// A license is ONLY invalid when the server says so explicitly, with one of
// the definitive verdicts: hwid_mismatch, revoked, or invalid_key.
// Everything else — offline, DNS failure, timeout, 5xx, transport error —
// is NOT a verdict: it means we could not reach the server, which says
// nothing about the license's validity. Logging a user out because of a
// network blip would be wrong (a laptop briefly in a dead zone must keep
// working). Do not "simplify" this into clearing the session on any error.

package auth

import (
	"context"
	"log"
	"sync"
	"time"

	"retard-sorter/internal/hwid"
	"retard-sorter/internal/supabase"
)

// revalidateInterval fires the re-check. Package-level var so tests can
// shrink it; production stays 2 minutes.
var revalidateInterval = 2 * time.Minute

var (
	revalMu      sync.Mutex
	revalRunning bool
)

// StartLicenseRevalidator launches the 2-minute background re-check and
// returns a stop func for clean shutdown (cancels the ticker goroutine —
// no leak). Call it exactly once, right after the app enters the
// authenticated state (successful login, or a valid cached session restored
// at launch). If it is already running — e.g. the app re-enters the
// authenticated state without a full process restart — a second call is a
// no-op: there is never a second goroutine.
//
// onInvalid(reason) is invoked when the license becomes definitively
// invalid; the app must force the user back to the activation screen.
func StartLicenseRevalidator(sb *supabase.Client, onInvalid func(reason string)) func() {
	revalMu.Lock()
	if revalRunning {
		revalMu.Unlock()
		return func() {} // already running — never start a second copy
	}
	revalRunning = true
	revalMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(revalidateInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				revalidateOnce(ctx, sb, onInvalid)
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			wg.Wait()
			revalMu.Lock()
			revalRunning = false
			revalMu.Unlock()
		})
	}
}

// revalidateOnce performs one license re-check against the server.
// Timeouts on outbound calls are enforced inside the supabase client
// (10s, same convention everywhere).
func revalidateOnce(ctx context.Context, sb *supabase.Client, onInvalid func(string)) {
	sess, err := LoadSession()
	if err != nil {
		// No usable session (logged out or expired) — nothing to re-check.
		return
	}
	if sess.LicenseKey == "" {
		// No cached key to re-check with. Keep the session; try again next
		// tick (the key appears once the user logs in again).
		log.Println("revalidate: no license key cached in session, skipping")
		return
	}

	// Recompute (cheap: cached inside GetHWID) and re-check with the key
	// cached in the session — no user interaction required.
	hwidVal, err := hwid.GetHWID()
	if err != nil {
		log.Printf("revalidate: hwid error: %v — keeping license, retry next tick", err)
		return
	}

	result, err := sb.ActivateLicense(ctx, sess.LicenseKey, hwidVal)
	if err != nil {
		// Transport error / offline / timeout — NOT a verdict. Keep the
		// session and try again on the next tick.
		log.Printf("revalidate: check failed (offline/unreachable?): %v — keeping license, retry next tick", err)
		return
	}

	switch result {
	case "ok", "activated":
		// Still valid on this machine. Keep running.
		return
	case "hwid_mismatch":
		_ = ClearSession()
		onInvalid("This license is active on a different device. Contact support to reset it.")
	case "revoked":
		_ = ClearSession()
		onInvalid("This license has been revoked. Contact support.")
	case "invalid_key":
		_ = ClearSession()
		onInvalid("This license is no longer valid. Contact support.")
	default:
		// Unknown verdict (e.g. "invalid_hwid" — an internal/server
		// oddity, not a user-facing verdict). Log and keep the session;
		// re-check on the next tick.
		log.Printf("revalidate: unexpected verdict %q — keeping license", result)
	}
}
