package mcpserver

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/cutejerryc/deepseek-tool-mcp/internal/deepseek"
)

// SearchOutput is the model-facing shape of a search response.
type SearchOutput struct {
	Query            string
	Requested        int
	Returned         int
	NeedContent      bool
	ContentTruncated bool
	Results          []SearchItem
}

// SearchItem is one result in a SearchOutput.
type SearchItem struct {
	Rank             int
	Title            string
	URL              string
	Snippet          string
	Summary          string
	Content          string
	ContentTruncated bool
	SiteName         *string
	Date             *string
	Authority        int
	RerankScore      float64
}

// FetchOutput is the model-facing shape of a fetch response.
type FetchOutput struct {
	URL           string
	Title         string
	Content       string
	ContentChars  int
	OriginalChars int
	Truncated     bool
}

func buildSearchOutput(query string, requested int, needContent bool, results []deepseek.SearchResult) SearchOutput {
	out := SearchOutput{
		Query:       query,
		Requested:   requested,
		Returned:    len(results),
		NeedContent: needContent,
		Results:     make([]SearchItem, 0, len(results)),
	}

	budget := maxContentTotal
	for i, r := range results {
		item := SearchItem{
			Rank:        i + 1,
			Title:       r.Title,
			URL:         r.URL,
			Snippet:     r.Snippet,
			Summary:     r.Summary,
			SiteName:    r.SiteName,
			Date:        r.Date,
			Authority:   r.Authority,
			RerankScore: r.RerankScore,
		}

		if needContent && r.Content != "" {
			limit := maxContentPerItem
			if budget < limit {
				limit = budget
			}
			if limit <= 0 {
				item.ContentTruncated = true
				out.ContentTruncated = true
			} else {
				content, truncated := truncateRunes(r.Content, limit)
				item.Content = content
				item.ContentTruncated = truncated
				budget -= utf8.RuneCountInString(content)
				if truncated {
					out.ContentTruncated = true
				}
			}
		}

		out.Results = append(out.Results, item)
	}
	return out
}

func formatSearchText(out SearchOutput) string {
	var b strings.Builder
	b.WriteString("DeepSeek supplementary web search\n")
	fmt.Fprintf(&b, "Query: %s\n", cleanInline(out.Query))
	fmt.Fprintf(&b, "Requested: %d | Returned: %d\n", out.Requested, out.Returned)

	if out.Returned == 0 {
		b.WriteString("\nNo results. Broaden the query, remove site:/after: filters, or retry with fewer operators.")
		return b.String()
	}

	b.WriteString("\n")
	for _, item := range out.Results {
		fmt.Fprintf(&b, "%d. %s\n", item.Rank, display(item.Title, "(untitled)"))
		fmt.Fprintf(&b, "   URL: %s\n", item.URL)

		meta := make([]string, 0, 4)
		if item.SiteName != nil && strings.TrimSpace(*item.SiteName) != "" {
			meta = append(meta, "Site: "+cleanInline(*item.SiteName))
		}
		if item.Date != nil && strings.TrimSpace(*item.Date) != "" {
			meta = append(meta, "Published: "+cleanInline(*item.Date))
		}
		meta = append(meta, "Authority: "+authorityLabel(item.Authority))
		meta = append(meta, fmt.Sprintf("Score: %.2f", item.RerankScore))
		fmt.Fprintf(&b, "   %s\n", strings.Join(meta, " | "))

		switch {
		case strings.TrimSpace(item.Summary) != "":
			fmt.Fprintf(&b, "   Summary: %s\n", strings.TrimSpace(item.Summary))
		case strings.TrimSpace(item.Snippet) != "":
			fmt.Fprintf(&b, "   Snippet: %s\n", strings.TrimSpace(item.Snippet))
		}

		if item.Content != "" {
			b.WriteString("   Content:\n")
			b.WriteString(item.Content)
			if !strings.HasSuffix(item.Content, "\n") {
				b.WriteString("\n")
			}
		}
		if item.ContentTruncated {
			b.WriteString("   [content truncated]\n")
		}
		b.WriteString("\n")
	}

	if out.ContentTruncated {
		b.WriteString("Note: page text was truncated to keep the response within budget. Use deepseek_fetch on a URL for its full content.\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatFetchText(out FetchOutput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", display(out.Title, "(untitled)"))
	fmt.Fprintf(&b, "URL: %s\n", out.URL)
	b.WriteString("Fetched with DeepSeek Fetch (Markdown)\n")
	if out.Truncated {
		fmt.Fprintf(&b, "Length: %d chars (truncated from %d)\n", out.ContentChars, out.OriginalChars)
	} else {
		fmt.Fprintf(&b, "Length: %d chars\n", out.ContentChars)
	}
	b.WriteString("\n---\n\n")
	if strings.TrimSpace(out.Content) == "" {
		b.WriteString("(empty content)")
	} else {
		b.WriteString(out.Content)
	}
	return strings.TrimRight(b.String(), "\n")
}

func authorityLabel(authority int) string {
	switch {
	case authority < 0:
		return "unknown"
	case authority >= 3:
		return fmt.Sprintf("%d/4 (high)", authority)
	default:
		return fmt.Sprintf("%d/4", authority)
	}
}

func display(v, fallback string) string {
	v = cleanInline(v)
	if v == "" {
		return fallback
	}
	return v
}

func cleanInline(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.TrimSpace(s)
}
