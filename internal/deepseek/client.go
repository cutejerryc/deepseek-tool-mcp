package deepseek

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxResponseBytes = 32 << 20 // 32 MiB
	maxErrorSnippet  = 300
	maxRetryWait     = 10 * time.Second
)

// Options configures a Client.
type Options struct {
	APIKey      string
	BaseURL     string
	Timeout     time.Duration
	MaxAttempts int
	UserAgent   string
}

// Client calls DeepSeek's standalone search and fetch tools.
type Client struct {
	apiKey      string
	baseURL     string
	timeout     time.Duration
	maxAttempts int
	userAgent   string
	httpClient  *http.Client
	// sleep is replaceable in tests.
	sleep func(context.Context, time.Duration) error
}

// New validates options and returns a Client ready for use.
func New(opts Options) (*Client, error) {
	apiKey := strings.TrimSpace(opts.APIKey)
	if apiKey == "" {
		return nil, errors.New("DeepSeek API key is empty")
	}

	base := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if base == "" {
		base = DefaultBaseURL
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("invalid DeepSeek base URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("invalid DeepSeek base URL %q: scheme must be http or https", base)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("invalid DeepSeek base URL %q: missing host", base)
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	attempts := opts.MaxAttempts
	if attempts <= 0 {
		attempts = 3
	}
	ua := strings.TrimSpace(opts.UserAgent)
	if ua == "" {
		ua = "deepseek-search-mcp"
	}

	return &Client{
		apiKey:      apiKey,
		baseURL:     base,
		timeout:     timeout,
		maxAttempts: attempts,
		userAgent:   ua,
		httpClient: &http.Client{
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				ForceAttemptHTTP2:     true,
				MaxIdleConns:          10,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   15 * time.Second,
				ResponseHeaderTimeout: timeout,
			},
		},
		sleep: sleepContext,
	}, nil
}

// Search calls POST /v1/tools/search.
func (c *Client) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	if strings.TrimSpace(req.Query) == "" {
		return nil, errors.New("search query is empty")
	}
	if req.Count == 0 {
		req.Count = 10
	}

	var out SearchResponse
	if err := c.post(ctx, SearchPath, req, &out); err != nil {
		return nil, err
	}
	if out.Results == nil {
		out.Results = []SearchResult{}
	}
	return &out, nil
}

// Fetch calls POST /v1/tools/fetch.
func (c *Client) Fetch(ctx context.Context, rawURL string) (*FetchResponse, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, errors.New("fetch URL is empty")
	}

	payload := struct {
		URL string `json:"url"`
	}{URL: rawURL}

	var out FetchResponse
	if err := c.post(ctx, FetchPath, payload, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) post(ctx context.Context, path string, payload any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode DeepSeek request: %w", err)
	}

	attempts := c.maxAttempts
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			delay := retryDelay(attempt-1, lastErr)
			if err := c.sleep(ctx, delay); err != nil {
				return err
			}
		}

		lastErr = c.doOnce(ctx, path, body, out)
		if lastErr == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !isRetryable(lastErr) {
			return lastErr
		}
	}

	return fmt.Errorf("DeepSeek request failed after %d attempt(s): %w", attempts, lastErr)
}

func (c *Client) doOnce(ctx context.Context, path string, body []byte, out any) error {
	attemptCtx := ctx
	var cancel context.CancelFunc
	if c.timeout > 0 {
		attemptCtx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build DeepSeek request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("DeepSeek request timed out after %s: %w", c.timeout, err)
		}
		return fmt.Errorf("DeepSeek request failed: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read DeepSeek response: %w", err)
	}
	if len(data) > maxResponseBytes {
		return fmt.Errorf("DeepSeek response exceeded %d bytes", maxResponseBytes)
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return parseAPIError(resp, data)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return fmt.Errorf("DeepSeek returned an empty response (HTTP %d)", resp.StatusCode)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode DeepSeek response: %w", err)
	}
	return nil
}

func parseAPIError(resp *http.Response, data []byte) error {
	apiErr := &APIError{
		StatusCode: resp.StatusCode,
		RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
	}

	var payload struct {
		Error struct {
			Message string          `json:"message"`
			Type    string          `json:"type"`
			Code    json.RawMessage `json:"code"`
		} `json:"error"`
		Message string          `json:"message"`
		Code    json.RawMessage `json:"code"`
	}
	if err := json.Unmarshal(data, &payload); err == nil {
		apiErr.Message = strings.TrimSpace(payload.Error.Message)
		apiErr.Type = strings.TrimSpace(payload.Error.Type)
		apiErr.Code = rawToString(payload.Error.Code)
		if apiErr.Message == "" {
			apiErr.Message = strings.TrimSpace(payload.Message)
		}
		if apiErr.Code == "" {
			apiErr.Code = rawToString(payload.Code)
		}
	}

	if apiErr.Message == "" {
		apiErr.Message = truncateRunes(strings.ToValidUTF8(string(data), ""), maxErrorSnippet)
	}
	return apiErr
}

func rawToString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(strings.Trim(string(raw), `"`))
}

func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(v); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

func retryDelay(retry int, err error) time.Duration {
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.RetryAfter > 0 {
		if apiErr.RetryAfter > maxRetryWait {
			return maxRetryWait
		}
		return apiErr.RetryAfter
	}

	delay := 500 * time.Millisecond
	for i := 1; i < retry && delay < 8*time.Second; i++ {
		delay *= 2
	}
	if delay > 8*time.Second {
		delay = 8 * time.Second
	}
	return delay + time.Duration(rand.Int63n(int64(250*time.Millisecond)))
}

func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}

	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusTooManyRequests,
			http.StatusInternalServerError,
			http.StatusBadGateway,
			http.StatusServiceUnavailable,
			http.StatusGatewayTimeout:
			return true
		default:
			return false
		}
	}

	if errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr interface{ Timeout() bool }
	if errors.As(err, &netErr) {
		return true
	}
	var temporary interface{ Temporary() bool }
	if errors.As(err, &temporary) {
		return temporary.Temporary()
	}
	return false
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max])
}
