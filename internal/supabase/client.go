// File: internal/supabase/client.go
//
// HTTPS Supabase wrapper: PostgREST RPCs + the Auth REST API. Secrets come
// from the environment only — never hardcode SUPABASE_URL or the service
// key in source.

package supabase

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// requestTimeout applies to every outbound call (context + client level).
const requestTimeout = 10 * time.Second

// Client wraps HTTPS calls to a Supabase project.
type Client struct {
	baseURL string
	key     string // service role key, from SUPABASE_SERVICE_KEY
	http    *http.Client
}

// New builds the client from the SUPABASE_URL and SUPABASE_SERVICE_KEY
// environment variables. Returns an error if either is missing or the URL
// is not https — no request is ever made over plain HTTP.
func New() (*Client, error) {
	u := strings.TrimRight(os.Getenv("SUPABASE_URL"), "/")
	k := os.Getenv("SUPABASE_SERVICE_KEY")
	if u == "" || k == "" {
		return nil, errors.New("SUPABASE_URL and SUPABASE_SERVICE_KEY environment variables are required")
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return nil, fmt.Errorf("invalid SUPABASE_URL: %w", err)
	}
	if parsed.Scheme != "https" {
		return nil, errors.New("SUPABASE_URL must use https://")
	}
	return &Client{
		baseURL: u,
		key:     k,
		http:    &http.Client{Timeout: requestTimeout},
	}, nil
}

// NewForTest constructs a client against an arbitrary base URL without the
// https requirement. Tests only — production entry point is New().
func NewForTest(baseURL, key string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		key:     key,
		http:    &http.Client{Timeout: requestTimeout},
	}
}

// ActivateLicense calls the activate_license RPC:
//
//	POST {SUPABASE_URL}/rest/v1/rpc/activate_license
//	{"p_key": key, "p_hwid": hwid}
//
// PostgREST returns the result as a JSON-encoded string (e.g. "ok",
// "revoked", "hwid_mismatch"). Exact, case-sensitive verdict strings are
// documented in the RPC contract; this function returns the raw string
// untouched.
func (c *Client) ActivateLicense(ctx context.Context, key, hwid string) (string, error) {
	body, err := json.Marshal(map[string]string{"p_key": key, "p_hwid": hwid})
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	resp, err := c.post(ctx, c.baseURL+"/rest/v1/rpc/activate_license", body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("activate_license: unexpected status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var result string
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", fmt.Errorf("activate_license: bad response body: %w", err)
	}
	return result, nil
}

func (c *Client) post(ctx context.Context, endpoint string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", c.key)
	req.Header.Set("Authorization", "Bearer "+c.key)
	return c.http.Do(req)
}
