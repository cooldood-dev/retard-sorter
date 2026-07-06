package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const Version = "1.0.0"

//go:embed static/*
var staticFiles embed.FS

type Config struct {
	Filters []string `json:"filters"`
	LastRun string   `json:"last_run"`
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
	Errors          int            `json:"errors"`
	ElapsedSeconds  float64        `json:"elapsed_seconds"`
	FiltersUsed     []string       `json:"filters_used"`
	SourceDirectory string         `json:"source_directory"`
	DomainBreakdown map[string]int `json:"domain_breakdown"`
	OutputDir       string         `json:"output_dir"`
}

type RunEntry struct {
	RunID          string         `json:"run_id"`
	Timestamp      string         `json:"timestamp"`
	FilesScanned   int            `json:"files_scanned"`
	TotalCookies   int            `json:"total_cookies"`
	TotalHits      int            `json:"total_hits"`
	Errors         int            `json:"errors"`
	ElapsedSeconds float64        `json:"elapsed_seconds"`
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
}

var (
	cfg       *Config
	cfgMu     sync.RWMutex
	cfgPath   string
	resDir    string
	assetsDir string
)

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
	os.MkdirAll(resDir, 0755)
	os.MkdirAll(assetsDir, 0755)
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
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cookies []Cookie
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) >= 7 {
			c := Cookie{
				RawDomain: parts[0],
				Domain:    strings.TrimLeft(parts[0], "."),
				Flag:      parts[1],
				Path:      parts[2],
				Secure:    parts[3],
				Expiry:    parts[4],
				Name:      parts[5],
				Value:     strings.Join(parts[6:], "\t"),
			}
			cookies = append(cookies, c)
		}
	}
	return cookies, nil
}

func cookieMatchesAnyDomain(c Cookie, targets []string) string {
	cd := strings.ToLower(c.Domain)
	raw := strings.ToLower(c.RawDomain)
	for _, td := range targets {
		tdl := strings.ToLower(td)
		if cd == tdl || strings.HasSuffix(cd, "."+tdl) {
			return td
		}
		r := strings.TrimLeft(raw, ".")
		if r == tdl || strings.HasSuffix(raw, "."+tdl) {
			return td
		}
	}
	return ""
}

// === HTTP HANDLERS ===

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
	sort.Slice(runs, func(i, j int) bool {
		return runs[i].RunID > runs[j].RunID
	})
	writeJSON(w, runs)
}

func handleRunDetail(w http.ResponseWriter, r *http.Request, runID string) {
	sumPath := filepath.Join(resDir, "run_"+runID, "summary.json")
	data, err := os.ReadFile(sumPath)
	if err != nil {
		http.Error(w, "not found", 404)
		return
	}
	var s Summary
	if json.Unmarshal(data, &s) != nil {
		http.Error(w, "invalid summary", 500)
		return
	}
	writeJSON(w, s)
}

func handleRunProgress(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("dir")
	if dir == "" {
		http.Error(w, "missing dir parameter", 400)
		return
	}

	cfgMu.RLock()
	filters := make([]string, len(cfg.Filters))
	copy(filters, cfg.Filters)
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

	var files []string
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		files = append(files, path)
		return nil
	})
	sort.Strings(files)

	if len(files) == 0 {
		writeSSE(w, ProgressEvent{Type: "error", Message: "no files in directory"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)

	now := time.Now()
	timestamp := now.Format("20060102_150405")
	runDir := filepath.Join(resDir, "run_"+timestamp)
	hitsDir := filepath.Join(runDir, "hits")
	os.MkdirAll(hitsDir, 0755)

	totalFiles := len(files)
	totalCookies := 0
	totalHits := 0
	domainHits := make(map[string]int)
	errors := 0
	startTime := time.Now()

	for idx, filePath := range files {
		relPath, _ := filepath.Rel(dir, filePath)
		cookies, err := parseNetscapeCookieFile(filePath)
		if err != nil {
			errors++
			fmt.Fprintf(w, "data: %s\n\n", mustJSON(ProgressEvent{
				Type: "progress", Processed: idx + 1, Total: totalFiles,
				Hits: totalHits, Percent: float64(idx+1) / float64(totalFiles) * 100,
			}))
			flusher.Flush()
			continue
		}

		totalCookies += len(cookies)
		var fileHits []Cookie
		for _, c := range cookies {
			matchedDomain := cookieMatchesAnyDomain(c, filters)
			if matchedDomain != "" {
				fileHits = append(fileHits, c)
				domainHits[matchedDomain]++
				totalHits++
			}
		}

		if len(fileHits) > 0 {
			outRel := strings.ReplaceAll(relPath, " ", "_")
			outRel = strings.ReplaceAll(outRel, "/", "\\")
			outPath := filepath.Join(hitsDir, outRel)
			os.MkdirAll(filepath.Dir(outPath), 0755)
			var sb strings.Builder
			sb.WriteString("# Netscape HTTP Cookie File\n")
			sb.WriteString(fmt.Sprintf("# Source: %s\n", filePath))
			sb.WriteString(fmt.Sprintf("# Matched domains: %d cookies\n", len(fileHits)))
			sb.WriteString(fmt.Sprintf("# Exported at: %s\n\n", now.Format(time.RFC3339)))
			for _, c := range fileHits {
				sb.WriteString(fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					c.RawDomain, c.Flag, c.Path, c.Secure, c.Expiry, c.Name, c.Value))
			}
			os.WriteFile(outPath, []byte(sb.String()), 0644)
		}

		os.Remove(filePath)

		parent := filepath.Dir(filePath)
		for parent != dir {
			entries, _ := os.ReadDir(parent)
			if len(entries) == 0 {
				os.Remove(parent)
				parent = filepath.Dir(parent)
			} else {
				break
			}
		}

		fmt.Fprintf(w, "data: %s\n\n", mustJSON(ProgressEvent{
			Type: "progress", Processed: idx + 1, Total: totalFiles,
			Hits: totalHits, Percent: float64(idx+1) / float64(totalFiles) * 100,
		}))
		flusher.Flush()
	}

	elapsed := time.Since(startTime).Seconds()

	summary := Summary{
		RunID:           timestamp,
		Timestamp:       now.Format(time.RFC3339),
		FilesScanned:    totalFiles,
		TotalCookies:    totalCookies,
		TotalHits:       totalHits,
		Errors:          errors,
		ElapsedSeconds:  elapsed,
		FiltersUsed:     filters,
		SourceDirectory: dir,
		DomainBreakdown: domainHits,
		OutputDir:       runDir,
	}
	sumData, _ := json.MarshalIndent(summary, "", "  ")
	os.WriteFile(filepath.Join(runDir, "summary.json"), sumData, 0644)

	cfgMu.Lock()
	cfg.LastRun = timestamp
	saveConfig(cfg)
	cfgMu.Unlock()

	fmt.Fprintf(w, "data: %s\n\n", mustJSON(ProgressEvent{
		Type:            "complete",
		RunID:           timestamp,
		FilesScanned:    totalFiles,
		TotalCookies:    totalCookies,
		TotalHits:       totalHits,
		Errors:          errors,
		ElapsedSeconds:  elapsed,
		OutputDir:       runDir,
		DomainBreakdown: domainHits,
	}))
	flusher.Flush()
}

// === HELPERS ===

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeSSE(w http.ResponseWriter, ev ProgressEvent) {
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
	switch {
	case len(os.Getenv("DISPLAY")) > 0:
		cmd = "xdg-open"
	case len(os.Getenv("TERM_PROGRAM")) > 0:
		cmd = "open"
	default:
		cmd = "cmd"
		args = []string{"/c", "start", url}
	}
	os.StartProcess(cmd, append(args, url), &os.ProcAttr{Files: []*os.File{nil, nil, nil}})
}

func main() {
	cfg = loadConfig()

	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err)
	}

	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(sub))))
	mux.Handle("/assets/", http.StripPrefix("/assets/",
		http.FileServer(http.Dir(assetsDir))))
	mux.HandleFunc("/api/config", handleConfig)
	mux.HandleFunc("/api/config/filters", handleUpdateFilters)
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

	go openBrowser("http://" + addr)

	if err := http.ListenAndServe(addr, mux); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %s\n", err)
		os.Exit(1)
	}
}
