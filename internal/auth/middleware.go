// File: internal/auth/middleware.go

package auth

import (
	"net/http"
	"net/url"
	"sync"
)

// RequireSession guards a handler behind an activated session.
//
// Missing, unreadable/corrupt, or keyless sessions all redirect to
// /activate — with the reason appended when a license has been forced
// invalid by the background revalidator (see SetForcedActivationReason), so
// the user lands back on the activation screen with an explanation.
//
// HARD RULE: any error from LoadSession is treated as "not activated" —
// fail closed, never treat an error as "activated".
func RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := LoadSession(); err != nil {
			if reason := ForcedActivationReason(); reason != "" {
				http.Redirect(w, r, "/activate?reason="+url.QueryEscape(reason), http.StatusFound)
				return
			}
			http.Redirect(w, r, "/activate", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// — forced-activation state ----------------------------------------------
//
// When the license is definitively invalidated (revoked / hwid_mismatch /
// invalid_key), revalidate.go clears the session and the app must bounce the
// user back to the activation screen. The reason is stored here so the next
// guarded request redirects to /activate with an explanation.

var (
	forcedMu     sync.Mutex
	forcedReason string
)

// SetForcedActivationReason records why the user was kicked back to the
// activation screen ("" clears it).
func SetForcedActivationReason(reason string) {
	forcedMu.Lock()
	defer forcedMu.Unlock()
	forcedReason = reason
}

// ForcedActivationReason returns the current forced reason ("" if none).
func ForcedActivationReason() string {
	forcedMu.Lock()
	defer forcedMu.Unlock()
	return forcedReason
}

// ClearForcedActivation clears the forced state (called after a successful
// activation).
func ClearForcedActivation() {
	SetForcedActivationReason("")
}
