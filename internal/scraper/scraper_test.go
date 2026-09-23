package scraper

import (
	"net/url"
	"strings"
	"testing"
)

func mustParseURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return u
}

func TestURLResolution(t *testing.T) {
	pageURL := mustParseURL("https://example.com/gallery/sub/page.html")

	tests := []struct {
		raw      string
		expected string
	}{
		{
			raw:      "photo.jpg",
			expected: "https://example.com/gallery/sub/photo.jpg",
		},
		{
			raw:      "/images/root.png",
			expected: "https://example.com/images/root.png",
		},
		{
			raw:      "../parent.webp",
			expected: "https://example.com/gallery/parent.webp",
		},
		{
			raw:      "https://cdn.example.org/absolute.jpg",
			expected: "https://cdn.example.org/absolute.jpg",
		},
		{
			raw:      "//cdn.example.net/protocol_relative.gif",
			expected: "https://cdn.example.net/protocol_relative.gif",
		},
		{
			raw:      "photo.jpg#section",
			expected: "https://example.com/gallery/sub/photo.jpg",
		},
		{
			raw:      "photo.jpg?query=1&token=abc",
			expected: "https://example.com/gallery/sub/photo.jpg?query=1&token=abc",
		},
	}

	for _, tt := range tests {
		resolved := resolveURL(pageURL, tt.raw)
		if resolved == nil {
			t.Fatalf("failed to resolve %q", tt.raw)
		}
		if resolved.String() != tt.expected {
			t.Errorf("resolveURL(%q) = %q, expected %q", tt.raw, resolved.String(), tt.expected)
		}
	}
}

func TestExtractionAndRanking(t *testing.T) {
	htmlContent := `
<!DOCTYPE html>
<html>
<head>
    <meta property="og:image" content="https://example.com/meta-og.jpg">
    <meta name="twitter:image" content="/meta-tw.png">
    <style>
        .banner { background-image: url('/css-bg.webp'); }
    </style>
    <script type="application/ld+json">
    {
        "@context": "https://schema.org",
        "@graph": [
            {
                "@type": "ImageObject",
                "contentUrl": "https://example.com/jsonld-graph.jpg"
            },
            {
                "@type": "NewsArticle",
                "image": [
                    "https://example.com/jsonld-array1.png",
                    "https://example.com/jsonld-array2.png"
                ]
            }
        ]
    }
    </script>
</head>
<body>
    <div style="background: url('inline-bg.jpg')"></div>

    <!-- Enclosing anchor with direct image should beat thumb -->
    <a href="/original/photo.jpg">
        <img src="/thumb/photo-300.jpg" alt="test">
    </a>

    <!-- Picture element with srcset should beat img src -->
    <picture>
        <source srcset="/pic-small.jpg 400w, /pic-large.jpg 1600w">
        <img src="/pic-fallback.jpg">
    </picture>

    <!-- Data-original should beat src -->
    <img src="/lazy-thumb.jpg" data-original="/full/original.png">

    <!-- Standalone anchor pointing to raster image -->
    <a href="/standalone.avif">Direct Download</a>

    <!-- Non-image link should NOT be extracted -->
    <a href="/about-us.html">About</a>
</body>
</html>
`
	pageURL := mustParseURL("https://example.com/gallery/index.html")
	candidates, err := Scrape(strings.NewReader(htmlContent), pageURL)
	if err != nil {
		t.Fatalf("Scrape failed: %v", err)
	}

	foundMap := make(map[string]*Candidate)
	for _, c := range candidates {
		foundMap[c.ResolvedURL.String()] = c
	}

	// 1. Check anchor beating thumbnail
	if _, ok := foundMap["https://example.com/original/photo.jpg"]; !ok {
		t.Errorf("expected original/photo.jpg to be extracted")
	}
	if _, ok := foundMap["https://example.com/thumb/photo-300.jpg"]; ok {
		t.Errorf("thumb/photo-300.jpg should have been replaced by original/photo.jpg")
	}

	// 2. Check picture source largest srcset beating fallback
	if _, ok := foundMap["https://example.com/pic-large.jpg"]; !ok {
		t.Errorf("expected pic-large.jpg to be extracted from picture")
	}
	if _, ok := foundMap["https://example.com/pic-fallback.jpg"]; ok {
		t.Errorf("pic-fallback.jpg should have been replaced by pic-large.jpg")
	}

	// 3. Check data-original beating lazy-thumb
	if _, ok := foundMap["https://example.com/full/original.png"]; !ok {
		t.Errorf("expected full/original.png to be extracted")
	}
	if _, ok := foundMap["https://example.com/lazy-thumb.jpg"]; ok {
		t.Errorf("lazy-thumb.jpg should have been replaced by full/original.png")
	}

	// 4. Check standalone anchor
	if _, ok := foundMap["https://example.com/standalone.avif"]; !ok {
		t.Errorf("expected standalone.avif to be extracted")
	}

	// 5. Check meta tags
	if _, ok := foundMap["https://example.com/meta-og.jpg"]; !ok {
		t.Errorf("expected meta-og.jpg to be extracted")
	}
	if _, ok := foundMap["https://example.com/meta-tw.png"]; !ok {
		t.Errorf("expected meta-tw.png to be extracted")
	}

	// 6. Check JSON-LD graph & arrays
	if _, ok := foundMap["https://example.com/jsonld-graph.jpg"]; !ok {
		t.Errorf("expected jsonld-graph.jpg to be extracted")
	}
	if _, ok := foundMap["https://example.com/jsonld-array1.png"]; !ok {
		t.Errorf("expected jsonld-array1.png to be extracted")
	}
	if _, ok := foundMap["https://example.com/jsonld-array2.png"]; !ok {
		t.Errorf("expected jsonld-array2.png to be extracted")
	}

	// 7. Check CSS styles
	if _, ok := foundMap["https://example.com/css-bg.webp"]; !ok {
		t.Errorf("expected css-bg.webp to be extracted from style block")
	}
	if _, ok := foundMap["https://example.com/gallery/inline-bg.jpg"]; !ok {
		t.Errorf("expected inline-bg.jpg to be extracted from inline style")
	}

	// 8. Verify non-image link is NOT extracted
	if _, ok := foundMap["https://example.com/about-us.html"]; ok {
		t.Errorf("about-us.html must NOT be extracted")
	}
}
