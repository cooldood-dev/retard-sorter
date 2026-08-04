package converter

import (
	"bufio"
	"context"
	"crypto/tls"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func proxyTransport(proxy string) *http.Transport {
	if proxy == "" {
		return &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, ResponseHeaderTimeout: 6 * time.Second}
	}
	u, err := url.Parse(proxy)
	if err != nil {
		return nil
	}
	return &http.Transport{
		Proxy:                 http.ProxyURL(u),
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
		ResponseHeaderTimeout: 6 * time.Second,
	}
}

func TestProbeBrowserLive(t *testing.T) {
	email := os.Getenv("PROBE_EMAIL")
	pass := os.Getenv("PROBE_PASSWORD")
	if email == "" || pass == "" {
		t.Skip("manual probe: set PROBE_EMAIL and PROBE_PASSWORD")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	b, err := LaunchBrowser(ctx, BrowserOpts{})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	defer b.Close()
	sleepFn := func() {
		time.Sleep(2 * time.Second)
	}
	cks, tok, st, err := LoginLive(ctx, b, email, pass, sleepFn, nil)
	t.Logf("status: %s, err: %v, token: %v, cookies: %d", st, err, tok != "", len(cks))
}

func TestProbeHTTPLive(t *testing.T) {
	email := os.Getenv("PROBE_EMAIL")
	pass := os.Getenv("PROBE_PASSWORD")
	if email == "" || pass == "" {
		t.Skip("manual probe: set PROBE_EMAIL and PROBE_PASSWORD")
	}
	client := NewHTTPClient(nil)
	cks, tok, st := HTTPLoginLive(client, email, pass)
	t.Logf("status: %s, token: %v, cookies: %d", st, tok != "", len(cks))
}

func TestProbeProxyHealth(t *testing.T) {
	if os.Getenv("PROBE_HTTP") == "" {
		t.Skip("manual probe: set PROBE_HTTP=1 to run")
	}
	path := os.Getenv("PROBE_FILE")
	if path == "" {
		t.Skip("set PROBE_FILE")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var raws []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			raws = append(raws, l)
		}
	}
	var proxs []string
	for _, r := range raws {
		if strings.Contains(r, "://") {
			proxs = append(proxs, r)
			continue
		}
		p := strings.Split(r, ":")
		if len(p) == 2 {
			proxs = append(proxs, "http://"+p[0]+":"+p[1])
		} else if len(p) == 4 {
			proxs = append(proxs, "http://"+p[2]+":"+p[3]+"@"+p[0]+":"+p[1])
		} else if len(p) == 3 {
			proxs = append(proxs, "http://"+p[2]+"@"+p[0]+":"+p[1])
		}
	}
	t.Logf("total proxies in file: %d", len(proxs))
	if len(proxs) > 60 {
		proxs = proxs[:60]
	}
	var (
		mu   sync.Mutex
		ok   int
		dead int
	)
	var wg sync.WaitGroup
	limit := make(chan struct{}, 30)
	for _, p := range proxs {
		limit <- struct{}{}
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			defer func() { <-limit }()
			tr := proxyTransport(p)
			if tr == nil {
				mu.Lock()
				dead++
				mu.Unlock()
				return
			}
			client := &http.Client{Transport: tr, Timeout: 8 * time.Second}
			req, _ := http.NewRequest("GET", "https://login.live.com/", nil)
			req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
			resp, err := client.Do(req)
			if err != nil || resp.StatusCode != 200 {
				mu.Lock()
				dead++
				mu.Unlock()
				if resp != nil {
					resp.Body.Close()
				}
				return
			}
			resp.Body.Close()
			mu.Lock()
			ok++
			mu.Unlock()
		}(p)
	}
	wg.Wait()
	t.Logf("alive: %d, dead: %d (of %d tested)", ok, dead, len(proxs))
}
