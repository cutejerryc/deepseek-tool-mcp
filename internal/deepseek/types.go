// Package deepseek implements a small client for DeepSeek's official
// web search and fetch tool APIs.
package deepseek

import (
	"fmt"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is the production DeepSeek API origin.
	DefaultBaseURL = "https://api.deepseek.com"
	// SearchPath is the standalone web search endpoint.
	SearchPath = "/v1/tools/search"
	// FetchPath is the standalone page fetch endpoint.
	FetchPath = "/v1/tools/fetch"
)

// SearchRequest mirrors POST /v1/tools/search.
type SearchRequest struct {
	Query        string `json:"query"`
	Count        int    `json:"count,omitempty"`
	OriQuestion  string `json:"ori_question,omitempty"`
	Localization string `json:"localization,omitempty"`
	NeedContent  bool   `json:"need_content,omitempty"`
}

// SearchResponse is the response body of POST /v1/tools/search.
type SearchResponse struct {
	Results []SearchResult `json:"results"`
}

// SearchResult is one ranked web result.
type SearchResult struct {
	RerankScore float64 `json:"rerank_score"`
	Title       string  `json:"title"`
	URL         string  `json:"url"`
	Snippet     string  `json:"snippet"`
	Summary     string  `json:"summary"`
	Content     string  `json:"content"`
	SiteName    *string `json:"site_name"`
	SiteIcon    *string `json:"site_icon"`
	Date        *string `json:"date"`
	Authority   int     `json:"authority"`
}

// FetchResponse is the response body of POST /v1/tools/fetch.
type FetchResponse struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

// APIError describes a non-2xx response from the DeepSeek API.
type APIError struct {
	StatusCode int
	Code       string
	Type       string
	Message    string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	parts := []string{fmt.Sprintf("DeepSeek API returned HTTP %d", e.StatusCode)}
	if e.Code != "" {
		parts = append(parts, "code="+e.Code)
	}
	if e.Type != "" {
		parts = append(parts, "type="+e.Type)
	}
	if e.Message != "" {
		parts = append(parts, e.Message)
	}
	return strings.Join(parts, ": ")
}
