package scraper

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func TestFindNextPage(t *testing.T) {
	pageURL := mustParseURL("https://example.com/gallery?page=1")

	tests := []struct {
		name     string
		html     string
		expected string // empty means nil expected
	}{
		{
			name:     "link rel next in head",
			html:     `<!DOCTYPE html><html><head><link rel="next" href="/gallery?page=2"></head><body></body></html>`,
			expected: "https://example.com/gallery?page=2",
		},
		{
			name:     "a rel next with multi-tokens",
			html:     `<!DOCTYPE html><html><body><a rel="nofollow next" href="/gallery?page=2">Go Forward</a></body></html>`,
			expected: "https://example.com/gallery?page=2",
		},
		{
			name:     "a aria-label Next",
			html:     `<!DOCTYPE html><html><body><a aria-label="Next Page" href="/gallery?page=2">&rarr;</a></body></html>`,
			expected: "https://example.com/gallery?page=2",
		},
		{
			name:     "a title Next",
			html:     `<!DOCTYPE html><html><body><a title="Следующая страница" href="/gallery?page=2">&rarr;</a></body></html>`,
			expected: "https://example.com/gallery?page=2",
		},
		{
			name:     "text heuristic inside nav element",
			html:     `<!DOCTYPE html><html><body><nav><a href="/gallery?page=2">Next &gt;</a></nav></body></html>`,
			expected: "https://example.com/gallery?page=2",
		},
		{
			name:     "text heuristic inside pagination class",
			html:     `<!DOCTYPE html><html><body><div class="pagination-bar"><a href="/gallery?page=2">:: next ::</a></div></body></html>`,
			expected: "https://example.com/gallery?page=2",
		},
		{
			name: "text heuristic with numeric siblings like ImageFap",
			html: `<!DOCTYPE html><html><body>
				<div class="expp-container" id="gallery">
					| <b>1</b> |
					<a href="?page=2">2</a> |
					<a href="?page=3">3</a> |
					<a href="?page=2">:: next ::</a>
				</div>
			</body></html>`,
			expected: "https://example.com/gallery?page=2",
		},
		{
			name:     "reject cross-origin next link",
			html:     `<!DOCTYPE html><html><head><link rel="next" href="https://other.com/gallery?page=2"></head><body></body></html>`,
			expected: "",
		},
		{
			name:     "reject bare chevron without next keyword",
			html:     `<!DOCTYPE html><html><body><a href="/gallery?page=2">&gt;</a></body></html>`,
			expected: "",
		},
		{
			name:     "reject next keyword in unrelated article context",
			html:     `<!DOCTYPE html><html><body><article><p>Read our <a href="/article/next-topic">next</a> article.</p></article></body></html>`,
			expected: "",
		},
		{
			name:     "reject self link with hash",
			html:     `<!DOCTYPE html><html><body><div class="pagination"><a href="#page1">Next</a></div></body></html>`,
			expected: "",
		},
		{
			name:     "strip fragment from next url",
			html:     `<!DOCTYPE html><html><head><link rel="next" href="/gallery?page=2#top"></head><body></body></html>`,
			expected: "https://example.com/gallery?page=2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, err := html.Parse(strings.NewReader(tt.html))
			if err != nil {
				t.Fatalf("html.Parse error: %v", err)
			}
			result := FindNextPage(doc, pageURL)
			if tt.expected == "" {
				if result != nil {
					t.Errorf("expected nil next page, got %s", result.String())
				}
			} else {
				if result == nil {
					t.Errorf("expected %s, got nil", tt.expected)
				} else if result.String() != tt.expected {
					t.Errorf("expected %s, got %s", tt.expected, result.String())
				}
			}
		})
	}
}

func TestFindNextPage_PriorityOrder(t *testing.T) {
	pageURL := mustParseURL("https://example.com/gallery?page=1")

	// Page with link rel=next (/page=2) AND a text next (/page=3)
	htmlContent := `
<!DOCTYPE html>
<html>
<head>
	<link rel="next" href="/gallery?page=2">
</head>
<body>
	<nav>
		<a href="/gallery?page=3">Next</a>
	</nav>
</body>
</html>
`
	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		t.Fatalf("html.Parse error: %v", err)
	}

	res := FindNextPage(doc, pageURL)
	if res == nil || res.String() != "https://example.com/gallery?page=2" {
		t.Errorf("expected priority link rel=next to win (page=2), got %v", res)
	}
}
