package deepseek

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	client, err := New(Options{
		APIKey:      "test-key",
		BaseURL:     baseURL,
		Timeout:     5 * time.Second,
		MaxAttempts: 3,
		UserAgent:   "deepseek-search-mcp/test",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	client.sleep = func(context.Context, time.Duration) error { return nil }
	return client
}

func TestNewValidation(t *testing.T) {
	if _, err := New(Options{APIKey: ""}); err == nil {
		t.Error("expected error for empty API key")
	}
	if _, err := New(Options{APIKey: "k", BaseURL: "ftp://example.com"}); err == nil {
		t.Error("expected error for non-http base URL")
	}
	if _, err := New(Options{APIKey: "k", BaseURL: "https://"}); err == nil {
		t.Error("expected error for base URL without host")
	}
}

func TestSearchRequestAndResponse(t *testing.T) {
	var gotAuth, gotContentType string
	var gotBody SearchRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != SearchPath {
			t.Errorf("path = %s, want %s", r.URL.Path, SearchPath)
		}
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")

		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"results": [
				{
					"rerank_score": 2.47,
					"title": "Example result",
					"url": "https://example.com/a",
					"snippet": "short",
					"summary": "long summary",
					"content": "",
					"site_name": "Example",
					"site_icon": "https://example.com/icon.png",
					"date": "2026-11-06T16:00:00+00:00",
					"authority": 4
				},
				{
					"rerank_score": 0.5,
					"title": "No metadata",
					"url": "https://example.com/b",
					"snippet": "s",
					"summary": "m",
					"content": "body",
					"site_name": null,
					"site_icon": null,
					"date": null,
					"authority": -1
				}
			]
		}`)
	}))
	defer srv.Close()

	client := testClient(t, srv.URL)
	resp, err := client.Search(context.Background(), SearchRequest{
		Query:       "deepseek",
		Count:       2,
		OriQuestion: "what is deepseek?",
		NeedContent: true,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want Bearer test-key", gotAuth)
	}
	if !strings.HasPrefix(gotContentType, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if gotBody.Query != "deepseek" || gotBody.Count != 2 || gotBody.OriQuestion != "what is deepseek?" || !gotBody.NeedContent {
		t.Errorf("request body = %+v", gotBody)
	}

	if len(resp.Results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(resp.Results))
	}
	if resp.Results[0].Title != "Example result" || resp.Results[0].Authority != 4 {
		t.Errorf("first result = %+v", resp.Results[0])
	}
	if resp.Results[0].SiteName == nil || *resp.Results[0].SiteName != "Example" {
		t.Errorf("site_name = %v, want Example", resp.Results[0].SiteName)
	}
	if resp.Results[1].SiteName != nil || resp.Results[1].Date != nil {
		t.Errorf("nullable fields should stay nil: %+v", resp.Results[1])
	}
}

func TestSearchDefaultsCount(t *testing.T) {
	var gotCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body SearchRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotCount = body.Count
		_, _ = io.WriteString(w, `{"results":[]}`)
	}))
	defer srv.Close()

	client := testClient(t, srv.URL)
	resp, err := client.Search(context.Background(), SearchRequest{Query: "q"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotCount != 10 {
		t.Errorf("count = %d, want default 10", gotCount)
	}
	if resp.Results == nil || len(resp.Results) != 0 {
		t.Errorf("results = %#v, want empty non-nil slice", resp.Results)
	}
}

func TestSearchAPIErrorIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid key","type":"authentication_error","code":"invalid_api_key"}}`)
	}))
	defer srv.Close()

	client := testClient(t, srv.URL)
	_, err := client.Search(context.Background(), SearchRequest{Query: "q"})
	if err == nil {
		t.Fatal("expected error")
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error type = %T, want *APIError: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusUnauthorized || apiErr.Code != "invalid_api_key" || apiErr.Message != "invalid key" {
		t.Errorf("APIError = %+v", apiErr)
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1 (no retry on 401)", calls.Load())
	}
	if !strings.Contains(err.Error(), "HTTP 401") {
		t.Errorf("error text = %q", err.Error())
	}
}

func TestSearchRetriesRateLimit(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"slow down"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"results":[{"title":"ok","url":"https://example.com","authority":3,"rerank_score":1.8}]}`)
	}))
	defer srv.Close()

	client := testClient(t, srv.URL)
	var delays []time.Duration
	client.sleep = func(_ context.Context, d time.Duration) error {
		delays = append(delays, d)
		return nil
	}

	resp, err := client.Search(context.Background(), SearchRequest{Query: "q"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2", calls.Load())
	}
	if len(delays) != 1 || delays[0] != time.Second {
		t.Errorf("delays = %v, want [1s] from Retry-After", delays)
	}
	if len(resp.Results) != 1 || resp.Results[0].Title != "ok" {
		t.Errorf("results = %+v", resp.Results)
	}
}

func TestSearchGivesUpAfterMaxAttempts(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"message":"unavailable"}}`)
	}))
	defer srv.Close()

	client := testClient(t, srv.URL)
	_, err := client.Search(context.Background(), SearchRequest{Query: "q"})
	if err == nil {
		t.Fatal("expected error")
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d, want 3", calls.Load())
	}
	if !strings.Contains(err.Error(), "after 3 attempt") {
		t.Errorf("error = %v", err)
	}
}

func TestFetch(t *testing.T) {
	var gotBody struct {
		URL string `json:"url"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != FetchPath {
			t.Errorf("path = %s, want %s", r.URL.Path, FetchPath)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = io.WriteString(w, `{"title":"Page","content":"# Hello\n\nworld"}`)
	}))
	defer srv.Close()

	client := testClient(t, srv.URL)
	resp, err := client.Fetch(context.Background(), "https://example.com/page")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if gotBody.URL != "https://example.com/page" {
		t.Errorf("url = %q", gotBody.URL)
	}
	if resp.Title != "Page" || resp.Content != "# Hello\n\nworld" {
		t.Errorf("response = %+v", resp)
	}
}

func TestParseRetryAfter(t *testing.T) {
	if d := parseRetryAfter("3"); d != 3*time.Second {
		t.Errorf("parseRetryAfter(3) = %s", d)
	}
	if d := parseRetryAfter(""); d != 0 {
		t.Errorf("parseRetryAfter(empty) = %s", d)
	}
	if d := parseRetryAfter("not-a-date"); d != 0 {
		t.Errorf("parseRetryAfter(invalid) = %s", d)
	}
}

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"429", &APIError{StatusCode: 429}, true},
		{"500", &APIError{StatusCode: 500}, true},
		{"503", &APIError{StatusCode: 503}, true},
		{"400", &APIError{StatusCode: 400}, false},
		{"401", &APIError{StatusCode: 401}, false},
		{"canceled", context.Canceled, false},
		{"deadline", context.DeadlineExceeded, true},
	}
	for _, tt := range tests {
		if got := isRetryable(tt.err); got != tt.want {
			t.Errorf("%s: isRetryable = %t, want %t", tt.name, got, tt.want)
		}
	}
}

func TestEmptyQueryRejected(t *testing.T) {
	client := testClient(t, "https://api.deepseek.com")
	if _, err := client.Search(context.Background(), SearchRequest{Query: "  "}); err == nil {
		t.Fatal("expected error for blank query")
	}
	if _, err := client.Fetch(context.Background(), "  "); err == nil {
		t.Fatal("expected error for blank URL")
	}
}
