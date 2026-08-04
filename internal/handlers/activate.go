// File: internal/handlers/activate.go

package handlers

import (
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	"retard-sorter/internal/auth"
	"retard-sorter/internal/hwid"
	"retard-sorter/internal/supabase"
)

// App carries the shared dependencies for all handlers in this package.
type App struct {
	SB  *supabase.Client
	Tpl *template.Template

	// OnActivated is invoked after a successful activation. main wires it
	// to start the license revalidator — the revalidator's single-start
	// guard makes repeated calls safe.
	OnActivated func()
}

// New builds the handler app. tpl must contain the "activate" template
// (see templates/activate.gohtml).
func New(sb *supabase.Client, tpl *template.Template) *App {
	return &App{SB: sb, Tpl: tpl}
}

type pageData struct {
	Error   string
	Message string
}

func (a *App) HandleActivate(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// Show the reason the user was forced back here (revalidation
		// invalidation) if one is pending.
		if reason := auth.ForcedActivationReason(); reason != "" {
			a.renderActivate(w, pageData{Message: reason})
			return
		}
		a.renderActivate(w, pageData{})
	case http.MethodPost:
		a.activatePost(w, r)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (a *App) renderActivate(w http.ResponseWriter, d pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.Tpl.ExecuteTemplate(w, "activate", d); err != nil {
		log.Printf("activate template error: %v", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
	}
}

func (a *App) activatePost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		a.renderActivate(w, pageData{Error: "Something went wrong, try again"})
		return
	}
	key := strings.TrimSpace(r.FormValue("license_key"))
	if key == "" {
		a.renderActivate(w, pageData{Error: "Please enter your license key."})
		return
	}

	hwidVal, err := hwid.GetHWID()
	if err != nil {
		log.Printf("activate: hwid error: %v", err)
		a.renderActivate(w, pageData{Error: "Something went wrong, try again"})
		return
	}

	result, err := a.SB.ActivateLicense(r.Context(), key, hwidVal)
	if err != nil {
		log.Printf("activate: rpc error: %v", err)
		a.renderActivate(w, pageData{Error: "Something went wrong, try again"})
		return
	}

	switch result {
	case "activated", "ok":
		// Newly activated, or already active on this device. Record the
		// activated key in the machine-bound session and enter the app.
		auth.ClearForcedActivation()
		if err := auth.SaveSession(auth.Session{
			LicenseKey:  key,
			ActivatedAt: time.Now(),
		}); err != nil {
			log.Printf("activate: save session failed: %v", err)
			a.renderActivate(w, pageData{Error: "Something went wrong, try again"})
			return
		}
		if a.OnActivated != nil {
			a.OnActivated()
		}
		http.Redirect(w, r, "/", http.StatusFound)
	case "invalid_key":
		a.renderActivate(w, pageData{Error: "This license key isn't valid."})
	case "revoked":
		a.renderActivate(w, pageData{Error: "This license has been revoked. Contact support."})
	case "hwid_mismatch":
		a.renderActivate(w, pageData{Error: "This license is already active on a different device. Contact support to reset it."})
	case "invalid_hwid":
		// Should never happen with a working GetHWID; it's a bug on our
		// side, not a user problem. Log it, show the generic message.
		log.Printf("activate: server reported invalid_hwid for key %q", key)
		a.renderActivate(w, pageData{Error: "Something went wrong, try again"})
	default:
		// Unknown verdict — log, generic message, never raw text to the user.
		log.Printf("activate: unexpected verdict %q for key %q", result, key)
		a.renderActivate(w, pageData{Error: "Something went wrong, try again"})
	}
}
