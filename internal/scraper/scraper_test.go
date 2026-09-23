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
	slots, err := Scrape(strings.NewReader(htmlContent), pageURL)
	if err != nil {
		t.Fatalf("Scrape failed: %v", err)
	}

	bestMap := make(map[string]*Slot)
	allCands := FlattenCandidates(slots)
	allURLs := make(map[string]bool)
	for _, c := range allCands {
		allURLs[c.ResolvedURL.String()] = true
	}
	for _, s := range slots {
		if best := s.Best(); best != nil {
			bestMap[best.ResolvedURL.String()] = s
		}
	}

	// 1. Check anchor beating thumbnail as best candidate, and thumbnail preserved as fallback
	slot1, ok := bestMap["https://example.com/original/photo.jpg"]
	if !ok {
		t.Errorf("expected original/photo.jpg to be best candidate in slot")
	} else {
		if len(slot1.Candidates) < 2 {
			t.Errorf("expected slot to contain thumbnail fallback, got %d candidates", len(slot1.Candidates))
		} else if slot1.Candidates[1].ResolvedURL.String() != "https://example.com/thumb/photo-300.jpg" {
			t.Errorf("expected second candidate to be thumb/photo-300.jpg, got %s", slot1.Candidates[1].ResolvedURL.String())
		}
	}

	// 2. Check picture source largest srcset beating fallback
	slot2, ok := bestMap["https://example.com/pic-large.jpg"]
	if !ok {
		t.Errorf("expected pic-large.jpg to be best candidate in picture slot")
	} else {
		hasFallback := false
		for _, c := range slot2.Candidates {
			if c.ResolvedURL.String() == "https://example.com/pic-fallback.jpg" {
				hasFallback = true
				break
			}
		}
		if !hasFallback {
			t.Errorf("expected picture slot to have pic-fallback.jpg as fallback")
		}
	}

	// 3. Check data-original beating lazy-thumb
	slot3, ok := bestMap["https://example.com/full/original.png"]
	if !ok {
		t.Errorf("expected full/original.png to be best candidate in slot")
	} else {
		if len(slot3.Candidates) < 2 || slot3.Candidates[1].ResolvedURL.String() != "https://example.com/lazy-thumb.jpg" {
			t.Errorf("expected slot to contain lazy-thumb.jpg as fallback")
		}
	}

	// 4. Check standalone anchor
	if _, ok := bestMap["https://example.com/standalone.avif"]; !ok {
		t.Errorf("expected standalone.avif to be extracted")
	}

	// 5. Check meta tags
	if _, ok := bestMap["https://example.com/meta-og.jpg"]; !ok {
		t.Errorf("expected meta-og.jpg to be extracted")
	}
	if _, ok := bestMap["https://example.com/meta-tw.png"]; !ok {
		t.Errorf("expected meta-tw.png to be extracted")
	}

	// 6. Check JSON-LD graph & arrays
	if !allURLs["https://example.com/jsonld-graph.jpg"] {
		t.Errorf("expected jsonld-graph.jpg to be extracted")
	}
	if !allURLs["https://example.com/jsonld-array1.png"] {
		t.Errorf("expected jsonld-array1.png to be extracted")
	}
	if !allURLs["https://example.com/jsonld-array2.png"] {
		t.Errorf("expected jsonld-array2.png to be extracted")
	}

	// 7. Check CSS styles
	if _, ok := bestMap["https://example.com/css-bg.webp"]; !ok {
		t.Errorf("expected css-bg.webp to be extracted from style block")
	}
	if _, ok := bestMap["https://example.com/gallery/inline-bg.jpg"]; !ok {
		t.Errorf("expected inline-bg.jpg to be extracted from inline style")
	}

	// 8. Verify non-image link is NOT extracted
	if allURLs["https://example.com/about-us.html"] {
		t.Errorf("about-us.html must NOT be extracted")
	}
}

func TestJSONLDStrictness(t *testing.T) {
	htmlContent := `
<!DOCTYPE html>
<html>
<head>
    <script type="application/ld+json">
    {
        "@context": "https://schema.org",
        "@type": "NewsArticle",
        "headline": "Breaking: Go 1.27 Released",
        "description": "A comprehensive article about the new Go release.",
        "author": {
            "@type": "Person",
            "name": "Jane Doe",
            "url": "https://example.com/authors/jane"
        },
        "publisher": {
            "@type": "Organization",
            "name": "Tech Times",
            "logo": {
                "@type": "ImageObject",
                "url": "https://example.com/logo.png"
            }
        },
        "image": "https://example.com/article-photo.jpg"
    }
    </script>
    <script type="application/ld+json">
    {
        "@context": "https://schema.org",
        "@type": "Product",
        "name": "Widget",
        "image": [
            "https://example.com/widget-1.jpg",
            {
                "@type": "ImageObject",
                "contentUrl": "https://example.com/widget-2.jpg",
                "thumbnailUrl": "https://example.com/widget-thumb.jpg"
            }
        ]
    }
    </script>
</head>
<body></body>
</html>
`
	pageURL := mustParseURL("https://example.com/news")
	slots, err := Scrape(strings.NewReader(htmlContent), pageURL)
	if err != nil {
		t.Fatalf("Scrape failed: %v", err)
	}

	allCands := FlattenCandidates(slots)
	candURLs := make(map[string]bool)
	for _, c := range allCands {
		candURLs[c.ResolvedURL.String()] = true
	}

	expectedAllowed := []string{
		"https://example.com/logo.png",
		"https://example.com/article-photo.jpg",
		"https://example.com/widget-1.jpg",
		"https://example.com/widget-2.jpg",
		"https://example.com/widget-thumb.jpg",
	}
	for _, exp := range expectedAllowed {
		if !candURLs[exp] {
			t.Errorf("expected JSON-LD image %q to be extracted", exp)
		}
	}

	forbidden := []string{
		"https://schema.org",
		"https://example.com/authors/jane",
		"Breaking: Go 1.27 Released",
		"NewsArticle",
		"Product",
		"Tech Times",
		"Jane Doe",
		"Widget",
	}
	for _, f := range forbidden {
		if candURLs[f] || candURLs["https://example.com/"+f] {
			t.Errorf("forbidden string %q was mistakenly extracted as candidate", f)
		}
	}

	if len(allCands) != len(expectedAllowed) {
		t.Errorf("expected exactly %d candidates, got %d: %v", len(expectedAllowed), len(allCands), candURLs)
	}
}
