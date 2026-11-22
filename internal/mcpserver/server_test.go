package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/cutejerryc/deepseek-tool-mcp/internal/deepseek"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestValidateURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"https", "https://example.com/a?b=c", false},
		{"http with port", "http://127.0.0.1:8080/x", false},
		{"empty", "", true},
		{"relative", "/just/a/path", true},
		{"ftp", "ftp://example.com/file", true},
		{"no host", "https:///path", true},
		{"credentials", "https://user:pass@example.com/", true},
		{"too long", "https://example.com/" + strings.Repeat("a", maxURLLength), true},
	}
	for _, tt := range tests {
		err := validateURL(tt.url)
		if (err != nil) != tt.wantErr {
			t.Errorf("%s: validateURL(%q) error = %v, wantErr %t", tt.name, tt.url, err, tt.wantErr)
		}
	}
}

func TestBuildSearchOutputContentBudget(t *testing.T) {
	big := strings.Repeat("好", 50000)
	results := make([]deepseek.SearchResult, 7)
	for i := range results {
		results[i] = deepseek.SearchResult{
			Title:   "t",
			URL:     "https://example.com",
			Content: big,
		}
	}

	out := buildSearchOutput("q", 7, true, results)
	total := 0
	for i, item := range out.Results {
		total += utf8.RuneCountInString(item.Content)
		if i < 6 {
			if !item.ContentTruncated {
				t.Errorf("result %d should be marked truncated", i)
			}
			if n := utf8.RuneCountInString(item.Content); n != maxContentPerItem {
				t.Errorf("result %d content = %d runes, want %d", i, n, maxContentPerItem)
			}
		}
	}
	if total != maxContentTotal {
		t.Errorf("total content = %d runes, want %d", total, maxContentTotal)
	}
	if out.Results[6].Content != "" || !out.Results[6].ContentTruncated {
		t.Errorf("last result should have content omitted and marked truncated: %+v", out.Results[6])
	}
	if !out.ContentTruncated {
		t.Error("output should be marked truncated")
	}
}

func TestBuildSearchOutputWithoutContent(t *testing.T) {
	out := buildSearchOutput("q", 1, false, []deepseek.SearchResult{{
		Title:   "t",
		URL:     "https://example.com",
		Content: "should not appear",
	}})
	if out.Results[0].Content != "" {
		t.Errorf("content = %q, want empty when need_content=false", out.Results[0].Content)
	}
	if out.ContentTruncated {
		t.Error("should not be marked truncated")
	}
}

func TestFormatSearchText(t *testing.T) {
	site := "Example"
	date := "2026-11-01T00:00:00Z"
	out := SearchOutput{
		Query:     "deepseek",
		Requested: 10,
		Returned:  1,
		Results: []SearchItem{{
			Rank:        1,
			Title:       "Result A",
			URL:         "https://example.com/a",
			Summary:     "summary text",
			SiteName:    &site,
			Date:        &date,
			Authority:   4,
			RerankScore: 2.5,
		}},
	}

	text := formatSearchText(out)
	for _, want := range []string{
		"DeepSeek supplementary web search",
		"Query: deepseek",
		"Requested: 10 | Returned: 1",
		"1. Result A",
		"URL: https://example.com/a",
		"Site: Example",
		"Published: 2026-11-01T00:00:00Z",
		"Authority: 4/4 (high)",
		"Score: 2.50",
		"Summary: summary text",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("formatted text missing %q:\n%s", want, text)
		}
	}
}

func TestFormatSearchTextEmpty(t *testing.T) {
	text := formatSearchText(SearchOutput{Query: "nothing", Requested: 5})
	if !strings.Contains(text, "No results.") {
		t.Errorf("unexpected empty-result text:\n%s", text)
	}
}

func TestFormatFetchText(t *testing.T) {
	out := FetchOutput{
		URL:           "https://example.com",
		Title:         "",
		Content:       "body",
		ContentChars:  4,
		OriginalChars: 10,
		Truncated:     true,
	}
	text := formatFetchText(out)
	for _, want := range []string{
		"# (untitled)",
		"URL: https://example.com",
		"Length: 4 chars (truncated from 10)",
		"body",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("formatted text missing %q:\n%s", want, text)
		}
	}
}

func TestMCPToolsEndToEnd(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case deepseek.SearchPath:
			var body deepseek.SearchRequest
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Query == "boom" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":{"message":"bad query"}}`)
				return
			}
			_, _ = io.WriteString(w, `{
				"results": [{
					"rerank_score": 2.1,
					"title": "Result A",
					"url": "https://example.com/a",
					"snippet": "short",
					"summary": "summary text",
					"content": "",
					"site_name": "Example",
					"site_icon": null,
					"date": "2026-11-01T00:00:00Z",
					"authority": 4
				}]
			}`)
		case deepseek.FetchPath:
			_, _ = io.WriteString(w, `{"title":"Fetched","content":"`+strings.Repeat("x", 2000)+`"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	client, err := deepseek.New(deepseek.Options{
		APIKey:      "test-key",
		BaseURL:     api.URL,
		Timeout:     5 * time.Second,
		MaxAttempts: 1,
		UserAgent:   "test",
	})
	if err != nil {
		t.Fatalf("deepseek.New: %v", err)
	}

	server := New(client, "test", log.New(io.Discard, "", 0))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	defer serverSession.Close()

	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	clientSession, err := mcpClient.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	defer clientSession.Close()

	tools, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	if !names[searchToolName] || !names[fetchToolName] {
		t.Errorf("tools = %v, want %s and %s", names, searchToolName, fetchToolName)
	}

	t.Run("search", func(t *testing.T) {
		res, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
			Name:      searchToolName,
			Arguments: map[string]any{"query": "deepseek", "count": 2},
		})
		if err != nil {
			t.Fatalf("CallTool: %v", err)
		}
		if res.IsError {
			t.Fatalf("tool error: %s", toolText(t, res))
		}
		text := toolText(t, res)
		for _, want := range []string{"Result A", "URL: https://example.com/a", "Authority: 4/4 (high)"} {
			if !strings.Contains(text, want) {
				t.Errorf("search text missing %q:\n%s", want, text)
			}
		}
	})

	t.Run("fetch truncation", func(t *testing.T) {
		res, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
			Name:      fetchToolName,
			Arguments: map[string]any{"url": "https://example.com/page", "max_chars": 1000},
		})
		if err != nil {
			t.Fatalf("CallTool: %v", err)
		}
		if res.IsError {
			t.Fatalf("tool error: %s", toolText(t, res))
		}
		text := toolText(t, res)
		if !strings.Contains(text, "truncated from 2000") {
			t.Errorf("fetch text missing truncation note:\n%s", text)
		}
	})

	t.Run("validation error", func(t *testing.T) {
		res, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
			Name:      searchToolName,
			Arguments: map[string]any{"query": "deepseek", "count": 99},
		})
		if err != nil {
			t.Fatalf("CallTool: %v", err)
		}
		if !res.IsError {
			t.Fatal("expected tool error for count=99")
		}
		if text := toolText(t, res); !strings.Contains(text, "count must be between 1 and 50") {
			t.Errorf("error text = %q", text)
		}
	})

	t.Run("api error", func(t *testing.T) {
		res, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
			Name:      searchToolName,
			Arguments: map[string]any{"query": "boom"},
		})
		if err != nil {
			t.Fatalf("CallTool: %v", err)
		}
		if !res.IsError {
			t.Fatal("expected tool error")
		}
		if text := toolText(t, res); !strings.Contains(text, "HTTP 400") {
			t.Errorf("error text = %q", text)
		}
	})
}

func toolText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	var parts []string
	for _, content := range res.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}
