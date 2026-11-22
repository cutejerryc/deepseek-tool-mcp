package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cutejerryc/deepseek-tool-mcp/internal/deepseek"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxQueryRunes      = 128
	maxCount           = 50
	defaultCount       = 10
	maxURLLength       = 8192
	defaultMaxChars    = 60000
	minMaxChars        = 1000
	maxMaxChars        = 200000
	maxContentPerItem  = 20000
	maxContentTotal    = 120000
	searchToolName     = "deepseek_search"
	fetchToolName      = "deepseek_fetch"
	serverInstructions = `TOOL PRIORITY: DeepSeek search/fetch is a SUPPLEMENTARY retrieval backend, not a first-line search tool.

- T0 (preferred): local Playwright/browser automation, interactive browsing, and local crawlers or fetchers. Use those first for any page reachable from the local machine.
- T1 (fallback): the deepseek_search and deepseek_fetch tools. Use them ONLY when local tooling cannot obtain the needed content: anti-bot or Cloudflare challenges, login/geo/IP restrictions, proxy-only access, or when a broad global web index is required.
- Never re-fetch content that local tools already returned successfully.
- Prefer deepseek_search first, then deepseek_fetch on the most promising URLs.`
)

// SearchInput is the input schema of deepseek_search.
type SearchInput struct {
	Query        string  `json:"query" jsonschema:"Search query (1-128 characters). Supports DeepSeek operators: site:example.com, -site:example.com, \"exact phrase\", -term, lang:zh,en, after:2025-01-01, before:2026-12-31."`
	Count        *int    `json:"count,omitempty" jsonschema:"Number of results to return (1-50). Default 10."`
	OriQuestion  *string `json:"ori_question,omitempty" jsonschema:"The user's original question (1-128 characters) to help the search engine infer intent. Optional."`
	Localization *string `json:"localization,omitempty" jsonschema:"Optional IP address used to localize results. Usually omit."`
	NeedContent  *bool   `json:"need_content,omitempty" jsonschema:"Include page body text for each result (default false). Increases latency and token usage; prefer deepseek_fetch on selected URLs."`
}

// FetchInput is the input schema of deepseek_fetch.
type FetchInput struct {
	URL      string `json:"url" jsonschema:"Absolute http(s) URL to read (maximum 8192 characters)."`
	MaxChars *int   `json:"max_chars,omitempty" jsonschema:"Maximum characters of Markdown to return (1000-200000). Default 60000."`
}

// New builds the MCP server with both tools registered.
func New(client *deepseek.Client, version string, logger *log.Logger) *mcp.Server {
	if logger == nil {
		logger = log.Default()
	}

	server := mcp.NewServer(&mcp.Implementation{
		Name:        "deepseek-search",
		Title:       "DeepSeek Web Search (supplementary)",
		Description: "Supplementary web search and URL reading backed by DeepSeek's official Search and Fetch APIs.",
		Version:     version,
	}, &mcp.ServerOptions{
		Instructions: serverInstructions,
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:  searchToolName,
		Title: "DeepSeek Web Search (supplementary)",
		Description: "SUPPLEMENTARY web search backed by DeepSeek's global index. Use ONLY when local Playwright/browser automation or crawlers cannot retrieve the content " +
			"(anti-bot challenges, geo/IP restrictions, proxy-only access, or a global index is required). Returns ranked results with title, URL, summary, site, " +
			"publish date, authority and optional page text. Supports site:, -site:, \"exact phrase\", -term, lang:, after: and before: operators.",
		Annotations: &mcp.ToolAnnotations{
			Title:          "DeepSeek Web Search (supplementary)",
			ReadOnlyHint:   true,
			IdempotentHint: true,
			OpenWorldHint:  boolPtr(true),
		},
	}, makeSearchHandler(client, logger))

	mcp.AddTool(server, &mcp.Tool{
		Name:  fetchToolName,
		Title: "DeepSeek URL Fetch (supplementary)",
		Description: "SUPPLEMENTARY URL reader backed by DeepSeek. Converts one http(s) page to Markdown. Use ONLY when local fetching fails " +
			"(anti-bot blocks, geo/IP restrictions, proxy-only access). Run deepseek_search first and fetch only the most promising URLs.",
		Annotations: &mcp.ToolAnnotations{
			Title:          "DeepSeek URL Fetch (supplementary)",
			ReadOnlyHint:   true,
			IdempotentHint: true,
			OpenWorldHint:  boolPtr(true),
		},
	}, makeFetchHandler(client, logger))

	return server
}

func makeSearchHandler(client *deepseek.Client, logger *log.Logger) mcp.ToolHandlerFor[SearchInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in SearchInput) (*mcp.CallToolResult, any, error) {
		query := strings.TrimSpace(in.Query)
		if query == "" {
			return nil, nil, errors.New("query must not be empty")
		}
		if !utf8.ValidString(query) {
			return nil, nil, errors.New("query must be valid UTF-8")
		}
		if utf8.RuneCountInString(query) > maxQueryRunes {
			return nil, nil, fmt.Errorf("query must be at most %d characters", maxQueryRunes)
		}

		count := defaultCount
		if in.Count != nil {
			count = *in.Count
		}
		if count < 1 || count > maxCount {
			return nil, nil, fmt.Errorf("count must be between 1 and %d", maxCount)
		}

		oriQuestion := ""
		if in.OriQuestion != nil {
			oriQuestion = strings.TrimSpace(*in.OriQuestion)
			if utf8.RuneCountInString(oriQuestion) > maxQueryRunes {
				return nil, nil, fmt.Errorf("ori_question must be at most %d characters", maxQueryRunes)
			}
		}

		localization := ""
		if in.Localization != nil {
			localization = strings.TrimSpace(*in.Localization)
		}

		needContent := in.NeedContent != nil && *in.NeedContent

		start := time.Now()
		logger.Printf("tool=%s count=%d need_content=%t query_chars=%d", searchToolName, count, needContent, utf8.RuneCountInString(query))

		resp, err := client.Search(ctx, deepseek.SearchRequest{
			Query:        query,
			Count:        count,
			OriQuestion:  oriQuestion,
			Localization: localization,
			NeedContent:  needContent,
		})
		if err != nil {
			logger.Printf("tool=%s error=%v elapsed=%s", searchToolName, err, time.Since(start).Round(time.Millisecond))
			return nil, nil, fmt.Errorf("deepseek_search failed: %w", err)
		}

		out := buildSearchOutput(query, count, needContent, resp.Results)
		logger.Printf("tool=%s ok results=%d elapsed=%s", searchToolName, out.Returned, time.Since(start).Round(time.Millisecond))

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: formatSearchText(out)}},
		}, nil, nil
	}
}

func makeFetchHandler(client *deepseek.Client, logger *log.Logger) mcp.ToolHandlerFor[FetchInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in FetchInput) (*mcp.CallToolResult, any, error) {
		rawURL := strings.TrimSpace(in.URL)
		if err := validateURL(rawURL); err != nil {
			return nil, nil, err
		}

		maxChars := defaultMaxChars
		if in.MaxChars != nil {
			maxChars = *in.MaxChars
		}
		if maxChars < minMaxChars || maxChars > maxMaxChars {
			return nil, nil, fmt.Errorf("max_chars must be between %d and %d", minMaxChars, maxMaxChars)
		}

		start := time.Now()
		logger.Printf("tool=%s url_host=%s max_chars=%d", fetchToolName, urlHost(rawURL), maxChars)

		resp, err := client.Fetch(ctx, rawURL)
		if err != nil {
			logger.Printf("tool=%s error=%v elapsed=%s", fetchToolName, err, time.Since(start).Round(time.Millisecond))
			return nil, nil, fmt.Errorf("deepseek_fetch failed: %w", err)
		}

		content, truncated := truncateRunes(resp.Content, maxChars)
		out := FetchOutput{
			URL:           rawURL,
			Title:         resp.Title,
			Content:       content,
			ContentChars:  utf8.RuneCountInString(content),
			OriginalChars: utf8.RuneCountInString(resp.Content),
			Truncated:     truncated,
		}
		logger.Printf("tool=%s ok chars=%d truncated=%t elapsed=%s", fetchToolName, out.ContentChars, truncated, time.Since(start).Round(time.Millisecond))

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: formatFetchText(out)}},
		}, nil, nil
	}
}

func validateURL(raw string) error {
	if raw == "" {
		return errors.New("url must not be empty")
	}
	if len(raw) > maxURLLength {
		return fmt.Errorf("url must be at most %d characters", maxURLLength)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("url must use http or https")
	}
	if u.Host == "" {
		return errors.New("url must include a host")
	}
	if u.User != nil {
		return errors.New("url must not contain credentials")
	}
	return nil
}

func urlHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "invalid"
	}
	return u.Host
}

func truncateRunes(s string, max int) (string, bool) {
	if max <= 0 {
		return "", s != ""
	}
	if utf8.RuneCountInString(s) <= max {
		return s, false
	}
	runes := []rune(s)
	return string(runes[:max]), true
}

func boolPtr(v bool) *bool { return &v }
