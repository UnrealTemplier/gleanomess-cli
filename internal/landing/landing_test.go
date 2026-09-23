package landing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/UnrealTemplier/gleanomess-cli/internal/downloader"
	"github.com/UnrealTemplier/gleanomess-cli/internal/scraper"
)

func mustParseURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return u
}

func TestExtractTokens(t *testing.T) {
	u := mustParseURL("https://example.com/photo/1624673182/?gid=9307846&page=0&view=2")
	tokens := ExtractTokens(u)

	tokenMap := make(map[string]bool)
	for _, tok := range tokens {
		tokenMap[tok] = true
	}

	if !tokenMap["1624673182"] {
		t.Errorf("expected token 1624673182, got %v", tokens)
	}
	if !tokenMap["9307846"] {
		t.Errorf("expected token 9307846, got %v", tokens)
	}
	if tokenMap["photo"] {
		t.Errorf("stop word 'photo' should be excluded")
	}
	if tokenMap["0"] || tokenMap["2"] {
		t.Errorf("small numbers 0 or 2 should be excluded")
	}
}

func TestLandingResolution_DirectRaster(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/image-landing" {
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write([]byte("fake-jpeg-data"))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	client, err := downloader.NewHTTPClient(server.URL)
	if err != nil {
		t.Fatalf("NewHTTPClient error: %v", err)
	}

	resolver := NewResolver(client, true)
	baseURL := mustParseURL(server.URL + "/gallery")
	landingURL := mustParseURL(server.URL + "/image-landing")

	slot := &scraper.Slot{
		LandingURL: landingURL,
		Candidates: []*scraper.Candidate{
			{
				RawURL:      "/thumb.jpg",
				ResolvedURL: mustParseURL(server.URL + "/thumb.jpg"),
				Source:      scraper.SourceImgSrc,
				Priority:    50,
			},
		},
	}

	resolver.ResolveSlots(context.Background(), []*scraper.Slot{slot}, baseURL)

	if len(slot.Candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(slot.Candidates))
	}

	promoted := slot.Candidates[0]
	if promoted.Source != scraper.SourceLandingImage {
		t.Errorf("expected SourceLandingImage, got %s", promoted.Source)
	}
	if promoted.Priority != 110 {
		t.Errorf("expected priority 110, got %d", promoted.Priority)
	}
	if promoted.ResolvedURL.String() != landingURL.String() {
		t.Errorf("expected %s, got %s", landingURL.String(), promoted.ResolvedURL.String())
	}
}

func TestLandingResolution_HTML_Correlation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/photo/1624673182":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(`
<!DOCTYPE html>
<html>
<body>
	<!-- Full original link with matching token -->
	<a href="/images/full/1624673182.jpg">
		<img src="/images/thumb/1624673182.jpg">
	</a>
	<!-- Unrelated logo -->
	<img src="/images/logo.png">
</body>
</html>
`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := downloader.NewHTTPClient(server.URL)
	if err != nil {
		t.Fatalf("NewHTTPClient error: %v", err)
	}

	resolver := NewResolver(client, true)
	baseURL := mustParseURL(server.URL + "/gallery")
	landingURL := mustParseURL(server.URL + "/photo/1624673182")

	slot := &scraper.Slot{
		LandingURL: landingURL,
		Candidates: []*scraper.Candidate{
			{
				RawURL:      server.URL + "/images/thumb/1624673182.jpg",
				ResolvedURL: mustParseURL(server.URL + "/images/thumb/1624673182.jpg"),
				Source:      scraper.SourceImgSrc,
				Priority:    50,
			},
		},
	}

	resolver.ResolveSlots(context.Background(), []*scraper.Slot{slot}, baseURL)

	if len(slot.Candidates) != 2 {
		t.Fatalf("expected 2 candidates after promotion, got %d", len(slot.Candidates))
	}

	promoted := slot.Candidates[0]
	if promoted.Source != scraper.SourceLandingImage {
		t.Errorf("expected SourceLandingImage, got %s", promoted.Source)
	}
	expectedFull := server.URL + "/images/full/1624673182.jpg"
	if promoted.ResolvedURL.String() != expectedFull {
		t.Errorf("expected full image %s, got %s", expectedFull, promoted.ResolvedURL.String())
	}
}

func TestLandingResolution_Deduplication(t *testing.T) {
	var requestCount int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/photo/common" {
			atomic.AddInt32(&requestCount, 1)
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`
<!DOCTYPE html><html><body>
	<a href="/images/full/item-84920.jpg"><img src="/thumb/item-84920.jpg"></a>
	<a href="/images/full/item-84921.jpg"><img src="/thumb/item-84921.jpg"></a>
</body></html>
`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	client, err := downloader.NewHTTPClient(server.URL)
	if err != nil {
		t.Fatalf("NewHTTPClient error: %v", err)
	}

	resolver := NewResolver(client, true)
	baseURL := mustParseURL(server.URL + "/gallery")
	commonLanding := mustParseURL(server.URL + "/photo/common")

	slot1 := &scraper.Slot{
		LandingURL: commonLanding,
		Candidates: []*scraper.Candidate{
			{
				RawURL:      "/thumb/item-84920.jpg",
				ResolvedURL: mustParseURL(server.URL + "/thumb/item-84920.jpg"),
				Source:      scraper.SourceImgSrc,
				Priority:    50,
			},
		},
	}

	slot2 := &scraper.Slot{
		LandingURL: commonLanding,
		Candidates: []*scraper.Candidate{
			{
				RawURL:      "/thumb/item-84921.jpg",
				ResolvedURL: mustParseURL(server.URL + "/thumb/item-84921.jpg"),
				Source:      scraper.SourceImgSrc,
				Priority:    50,
			},
		},
	}

	resolver.ResolveSlots(context.Background(), []*scraper.Slot{slot1, slot2}, baseURL)

	// Verify only 1 request was made for the landing page
	if atomic.LoadInt32(&requestCount) != 1 {
		t.Errorf("expected exactly 1 request to /photo/common, got %d", requestCount)
	}

	// Verify both slots got their respective correlated originals promoted
	if slot1.Candidates[0].ResolvedURL.String() != server.URL+"/images/full/item-84920.jpg" {
		t.Errorf("slot1 failed to correlate, got %s", slot1.Candidates[0].ResolvedURL.String())
	}
	if slot2.Candidates[0].ResolvedURL.String() != server.URL+"/images/full/item-84921.jpg" {
		t.Errorf("slot2 failed to correlate, got %s", slot2.Candidates[0].ResolvedURL.String())
	}
}

func TestLandingResolution_CrossOriginIgnored(t *testing.T) {
	client, err := downloader.NewHTTPClient("https://example.com/gallery")
	if err != nil {
		t.Fatalf("NewHTTPClient error: %v", err)
	}

	resolver := NewResolver(client, true)
	baseURL := mustParseURL("https://example.com/gallery")
	crossOriginLanding := mustParseURL("https://malicious.org/photo/12345")

	slot := &scraper.Slot{
		LandingURL: crossOriginLanding,
		Candidates: []*scraper.Candidate{
			{
				RawURL:      "/thumb.jpg",
				ResolvedURL: mustParseURL("https://example.com/thumb.jpg"),
				Source:      scraper.SourceImgSrc,
				Priority:    50,
			},
		},
	}

	resolver.ResolveSlots(context.Background(), []*scraper.Slot{slot}, baseURL)

	if len(slot.Candidates) != 1 {
		t.Errorf("expected cross origin landing to be ignored, got %d candidates", len(slot.Candidates))
	}
}

func TestLandingResolution_AmbiguousRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		// Landing page has multiple images with NO matching tokens to slot
		w.Write([]byte(`
<!DOCTYPE html><html><body>
	<img src="/img-a.jpg">
	<img src="/img-b.jpg">
</body></html>
`))
	}))
	defer server.Close()

	client, err := downloader.NewHTTPClient(server.URL)
	if err != nil {
		t.Fatalf("NewHTTPClient error: %v", err)
	}

	resolver := NewResolver(client, true)
	baseURL := mustParseURL(server.URL + "/gallery")
	landingURL := mustParseURL(server.URL + "/detail")

	slot := &scraper.Slot{
		LandingURL: landingURL,
		Candidates: []*scraper.Candidate{
			{
				RawURL:      "/thumb-xyz.jpg",
				ResolvedURL: mustParseURL(server.URL + "/thumb-xyz.jpg"),
				Source:      scraper.SourceImgSrc,
				Priority:    50,
			},
		},
	}

	resolver.ResolveSlots(context.Background(), []*scraper.Slot{slot}, baseURL)

	// Since there is no token correlation, candidate must NOT be promoted!
	if len(slot.Candidates) != 1 {
		t.Errorf("expected ambiguous/unrelated candidates to be rejected, got %d candidates", len(slot.Candidates))
	}
}

func TestLandingResolution_HTTPErrorFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Forbidden", http.StatusForbidden)
	}))
	defer server.Close()

	client, err := downloader.NewHTTPClient(server.URL)
	if err != nil {
		t.Fatalf("NewHTTPClient error: %v", err)
	}

	resolver := NewResolver(client, true)
	baseURL := mustParseURL(server.URL + "/gallery")
	landingURL := mustParseURL(server.URL + "/forbidden-photo")

	slot := &scraper.Slot{
		LandingURL: landingURL,
		Candidates: []*scraper.Candidate{
			{
				RawURL:      "/thumb.jpg",
				ResolvedURL: mustParseURL(server.URL + "/thumb.jpg"),
				Source:      scraper.SourceImgSrc,
				Priority:    50,
			},
		},
	}

	resolver.ResolveSlots(context.Background(), []*scraper.Slot{slot}, baseURL)

	// Slot must retain its fallback candidate without crash or promotion
	if len(slot.Candidates) != 1 {
		t.Errorf("expected slot to retain fallback candidate, got %d", len(slot.Candidates))
	}
	if slot.Candidates[0].ResolvedURL.String() != server.URL+"/thumb.jpg" {
		t.Errorf("fallback candidate changed unexpectedly")
	}
}
