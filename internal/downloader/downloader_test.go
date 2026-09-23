package downloader

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/UnrealTemplier/gleanomess-cli/internal/scraper"
)

func createSolidPNG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 100, G: 150, B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func TestSizeFiltering(t *testing.T) {
	png255x255 := createSolidPNG(255, 255)
	png256x255 := createSolidPNG(256, 255)
	png255x256 := createSolidPNG(255, 256)
	png256x256 := createSolidPNG(256, 256)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		switch r.URL.Path {
		case "/img_255x255.png":
			w.Write(png255x255)
		case "/img_256x255.png":
			w.Write(png256x255)
		case "/img_255x256.png":
			w.Write(png255x256)
		case "/img_256x256.png":
			w.Write(png256x256)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	tmpDir, err := os.MkdirTemp("", "gleanomess-filter-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	client, err := NewHTTPClient(ts.URL)
	if err != nil {
		t.Fatalf("NewHTTPClient failed: %v", err)
	}

	d := NewDownloader(client, Options{
		Workers:   2,
		LimitSize: 256,
		OutputDir: tmpDir,
		PageURL:   ts.URL,
	})

	slots := []*scraper.Slot{
		{Candidates: []*scraper.Candidate{{RawURL: "/img_255x255.png", ResolvedURL: mustParse(ts.URL + "/img_255x255.png"), Source: scraper.SourceImgSrc}}},
		{Candidates: []*scraper.Candidate{{RawURL: "/img_256x255.png", ResolvedURL: mustParse(ts.URL + "/img_256x255.png"), Source: scraper.SourceImgSrc}}},
		{Candidates: []*scraper.Candidate{{RawURL: "/img_255x256.png", ResolvedURL: mustParse(ts.URL + "/img_255x256.png"), Source: scraper.SourceImgSrc}}},
		{Candidates: []*scraper.Candidate{{RawURL: "/img_256x256.png", ResolvedURL: mustParse(ts.URL + "/img_256x256.png"), Source: scraper.SourceImgSrc}}},
	}

	summary, err := d.DownloadAll(context.Background(), slots)
	if err != nil {
		t.Fatalf("DownloadAll failed: %v", err)
	}

	if summary.Downloaded != 3 {
		t.Errorf("expected 3 downloaded images, got %d", summary.Downloaded)
	}
	if summary.Skipped != 1 {
		t.Errorf("expected 1 skipped image (255x255), got %d", summary.Skipped)
	}

	// 255x255 should not exist in output dir
	if _, err := os.Stat(filepath.Join(tmpDir, "img_255x255.png")); !os.IsNotExist(err) {
		t.Errorf("img_255x255.png should have been rejected and removed")
	}

	// 256x255, 255x256, 256x256 should exist
	for _, expectedName := range []string{"img_256x255.png", "img_255x256.png", "img_256x256.png"} {
		if _, err := os.Stat(filepath.Join(tmpDir, expectedName)); err != nil {
			t.Errorf("expected %s to exist in output directory", expectedName)
		}
	}
}

func TestRedirectCookiesReferer(t *testing.T) {
	var (
		cookieReceived atomic.Bool
		refererChecked atomic.Bool
	)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify Referer header
		if r.Header.Get("Referer") == "https://example.com/gallery" {
			refererChecked.Store(true)
		}

		// Check Cookie
		for _, c := range r.Cookies() {
			if c.Name == "session" && c.Value == "test-val" {
				cookieReceived.Store(true)
			}
		}

		if r.URL.Path == "/start" {
			http.SetCookie(w, &http.Cookie{
				Name:  "session",
				Value: "test-val",
				Path:  "/",
			})
			http.Redirect(w, r, "/end", http.StatusFound)
			return
		}

		if r.URL.Path == "/end" {
			w.Header().Set("Content-Type", "image/png")
			w.Write(createSolidPNG(10, 10))
			return
		}

		http.NotFound(w, r)
	}))
	defer ts.Close()

	client, err := NewHTTPClient("https://example.com/gallery")
	if err != nil {
		t.Fatalf("NewHTTPClient failed: %v", err)
	}

	res, err := client.GetWithRetry(context.Background(), ts.URL+"/start", true)
	if err != nil {
		t.Fatalf("GetWithRetry failed: %v", err)
	}
	defer res.Response.Body.Close()

	if !refererChecked.Load() {
		t.Errorf("expected Referer header to be set")
	}

	// Make another request to verify cookie is preserved across requests via Jar
	res2, err := client.GetWithRetry(context.Background(), ts.URL+"/end", true)
	if err != nil {
		t.Fatalf("second request failed: %v", err)
	}
	defer res2.Response.Body.Close()

	if !cookieReceived.Load() {
		t.Errorf("expected Cookie to be sent with subsequent request")
	}
}

func TestRetryTemporaryFailure(t *testing.T) {
	var attempts atomic.Int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := attempts.Add(1)
		if cur < 3 {
			// Fail first 2 attempts with 503
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write(createSolidPNG(20, 20))
	}))
	defer ts.Close()

	client, err := NewHTTPClient(ts.URL)
	if err != nil {
		t.Fatalf("NewHTTPClient failed: %v", err)
	}

	res, err := client.GetWithRetry(context.Background(), ts.URL+"/flaky", true)
	if err != nil {
		t.Fatalf("GetWithRetry should have succeeded after retries, got %v", err)
	}
	defer res.Response.Body.Close()

	if attempts.Load() != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts.Load())
	}
}

func TestPartialFailureDoesNotAbortJob(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/good.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write(createSolidPNG(50, 50))
		case "/notfound.png":
			w.WriteHeader(http.StatusNotFound)
		case "/corrupt.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write([]byte("not a real jpeg"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	tmpDir, err := os.MkdirTemp("", "gleanomess-partial-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	client, err := NewHTTPClient(ts.URL)
	if err != nil {
		t.Fatalf("NewHTTPClient failed: %v", err)
	}

	d := NewDownloader(client, Options{
		Workers:   2,
		OutputDir: tmpDir,
		PageURL:   ts.URL,
	})

	slots := []*scraper.Slot{
		{Candidates: []*scraper.Candidate{{RawURL: "/good.png", ResolvedURL: mustParse(ts.URL + "/good.png"), Source: scraper.SourceImgSrc}}},
		{Candidates: []*scraper.Candidate{{RawURL: "/notfound.png", ResolvedURL: mustParse(ts.URL + "/notfound.png"), Source: scraper.SourceImgSrc}}},
		{Candidates: []*scraper.Candidate{{RawURL: "/corrupt.jpg", ResolvedURL: mustParse(ts.URL + "/corrupt.jpg"), Source: scraper.SourceImgSrc}}},
	}

	summary, err := d.DownloadAll(context.Background(), slots)
	if err != nil {
		t.Fatalf("DownloadAll should not return fatal error on partial failures: %v", err)
	}

	if summary.Downloaded != 1 {
		t.Errorf("expected 1 downloaded image, got %d", summary.Downloaded)
	}
	if summary.Errors != 1 {
		t.Errorf("expected 1 HTTP error (404), got %d", summary.Errors)
	}
	if summary.Skipped != 1 {
		t.Errorf("expected 1 skipped image (corrupt), got %d", summary.Skipped)
	}

	// Verify good.png exists
	if _, err := os.Stat(filepath.Join(tmpDir, "good.png")); err != nil {
		t.Errorf("expected good.png to exist: %v", err)
	}
}

func TestCandidateFallback(t *testing.T) {
	// best candidate -> 404 failure
	// second candidate -> 200 success
	secondPNG := createSolidPNG(120, 80)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/broken-large.jpg":
			http.NotFound(w, r)
		case "/thumb.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write(secondPNG)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	tmpDir, err := os.MkdirTemp("", "gleanomess-fallback-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	client, err := NewHTTPClient(ts.URL)
	if err != nil {
		t.Fatalf("NewHTTPClient failed: %v", err)
	}

	d := NewDownloader(client, Options{
		Workers:   2,
		OutputDir: tmpDir,
		PageURL:   ts.URL,
	})

	slot := &scraper.Slot{
		Candidates: []*scraper.Candidate{
			{
				RawURL:      "/broken-large.jpg",
				ResolvedURL: mustParse(ts.URL + "/broken-large.jpg"),
				Source:      scraper.SourceAnchorImage,
				Priority:    100,
			},
			{
				RawURL:      "/thumb.png",
				ResolvedURL: mustParse(ts.URL + "/thumb.png"),
				Source:      scraper.SourceImgSrc,
				Priority:    50,
			},
		},
	}

	summary, err := d.DownloadAll(context.Background(), []*scraper.Slot{slot})
	if err != nil {
		t.Fatalf("DownloadAll failed: %v", err)
	}

	if summary.Downloaded != 1 {
		t.Errorf("expected 1 downloaded image after fallback, got %d", summary.Downloaded)
	}
	if summary.Errors != 0 {
		t.Errorf("expected 0 slot errors because fallback succeeded, got %d", summary.Errors)
	}

	// Verify thumb.png was saved
	if _, err := os.Stat(filepath.Join(tmpDir, "thumb.png")); err != nil {
		t.Errorf("expected thumb.png to exist in output directory: %v", err)
	}
}

func TestNoUnnecessaryFallback(t *testing.T) {
	// best candidate -> valid image
	// second candidate -> valid image
	// Verify ONLY the best candidate is downloaded, and the second candidate is NEVER requested
	var secondRequested atomic.Bool

	bestPNG := createSolidPNG(300, 200)
	secondPNG := createSolidPNG(100, 100)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/best.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write(bestPNG)
		case "/second.png":
			secondRequested.Store(true)
			w.Header().Set("Content-Type", "image/png")
			w.Write(secondPNG)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	tmpDir, err := os.MkdirTemp("", "gleanomess-no-unnecessary-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	client, err := NewHTTPClient(ts.URL)
	if err != nil {
		t.Fatalf("NewHTTPClient failed: %v", err)
	}

	d := NewDownloader(client, Options{
		Workers:   2,
		OutputDir: tmpDir,
		PageURL:   ts.URL,
	})

	slot := &scraper.Slot{
		Candidates: []*scraper.Candidate{
			{
				RawURL:      "/best.png",
				ResolvedURL: mustParse(ts.URL + "/best.png"),
				Source:      scraper.SourcePictureSource,
				Priority:    80,
			},
			{
				RawURL:      "/second.png",
				ResolvedURL: mustParse(ts.URL + "/second.png"),
				Source:      scraper.SourceImgSrc,
				Priority:    50,
			},
		},
	}

	summary, err := d.DownloadAll(context.Background(), []*scraper.Slot{slot})
	if err != nil {
		t.Fatalf("DownloadAll failed: %v", err)
	}

	if summary.Downloaded != 1 {
		t.Errorf("expected 1 downloaded image, got %d", summary.Downloaded)
	}

	// Verify second URL was never requested
	if secondRequested.Load() {
		t.Errorf("second candidate should NOT have been requested when best candidate succeeded")
	}

	// Verify best.png exists, second.png does not
	if _, err := os.Stat(filepath.Join(tmpDir, "best.png")); err != nil {
		t.Errorf("expected best.png to exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "second.png")); !os.IsNotExist(err) {
		t.Errorf("second.png should NOT exist")
	}
}

func TestLargeDownloadLimitAndNoSilentTruncation(t *testing.T) {
	// Set MaxImageBytes to 500 bytes for this test
	validBytes := createSolidPNG(10, 10) // ~70-100 bytes
	oversizedBytes := make([]byte, 1024) // 1024 bytes > 500 byte limit

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/normal.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write(validBytes)
		case "/oversized.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write(oversizedBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	tmpDir, err := os.MkdirTemp("", "gleanomess-limit-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	client, err := NewHTTPClient(ts.URL)
	if err != nil {
		t.Fatalf("NewHTTPClient failed: %v", err)
	}

	d := NewDownloader(client, Options{
		Workers:       2,
		OutputDir:     tmpDir,
		PageURL:       ts.URL,
		MaxImageBytes: 500, // 500 byte limit
	})

	slots := []*scraper.Slot{
		{Candidates: []*scraper.Candidate{{RawURL: "/normal.png", ResolvedURL: mustParse(ts.URL + "/normal.png"), Source: scraper.SourceImgSrc}}},
		{Candidates: []*scraper.Candidate{{RawURL: "/oversized.png", ResolvedURL: mustParse(ts.URL + "/oversized.png"), Source: scraper.SourceImgSrc}}},
	}

	summary, err := d.DownloadAll(context.Background(), slots)
	if err != nil {
		t.Fatalf("DownloadAll failed: %v", err)
	}

	if summary.Downloaded != 1 {
		t.Errorf("expected 1 downloaded image, got %d", summary.Downloaded)
	}
	if summary.Errors != 1 {
		t.Errorf("expected 1 error for oversized image, got %d", summary.Errors)
	}

	// Verify normal.png exists
	if _, err := os.Stat(filepath.Join(tmpDir, "normal.png")); err != nil {
		t.Errorf("expected normal.png to exist: %v", err)
	}

	// Verify oversized.png does NOT exist and no truncated file was saved
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() != "normal.png" {
			t.Errorf("unexpected file in output directory: %s", entry.Name())
		}
	}
}

func TestResponseBodyClosureOnErrors(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/400":
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("bad request"))
		case "/403":
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte("forbidden"))
		case "/404":
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte("not found"))
		case "/500":
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("server error"))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer ts.Close()

	client, err := NewHTTPClient(ts.URL)
	if err != nil {
		t.Fatalf("NewHTTPClient failed: %v", err)
	}

	for _, path := range []string{"/400", "/403", "/404", "/500"} {
		res, err := client.GetWithRetry(context.Background(), ts.URL+path, true)
		if err == nil {
			t.Errorf("expected error for %s", path)
		}
		if res != nil && res.Response != nil {
			t.Errorf("expected res.Response to be nil (body closed and discarded) on error for %s", path)
		}
	}
}

func mustParse(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(fmt.Sprintf("mustParse failed on %q: %v", s, err))
	}
	return u
}
