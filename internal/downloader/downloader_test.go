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

	cands := []*scraper.Candidate{
		{RawURL: "/img_255x255.png", ResolvedURL: mustParse(ts.URL + "/img_255x255.png"), Source: scraper.SourceImgSrc},
		{RawURL: "/img_256x255.png", ResolvedURL: mustParse(ts.URL + "/img_256x255.png"), Source: scraper.SourceImgSrc},
		{RawURL: "/img_255x256.png", ResolvedURL: mustParse(ts.URL + "/img_255x256.png"), Source: scraper.SourceImgSrc},
		{RawURL: "/img_256x256.png", ResolvedURL: mustParse(ts.URL + "/img_256x256.png"), Source: scraper.SourceImgSrc},
	}

	summary, err := d.DownloadAll(context.Background(), cands)
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

	cands := []*scraper.Candidate{
		{RawURL: "/good.png", ResolvedURL: mustParse(ts.URL + "/good.png"), Source: scraper.SourceImgSrc},
		{RawURL: "/notfound.png", ResolvedURL: mustParse(ts.URL + "/notfound.png"), Source: scraper.SourceImgSrc},
		{RawURL: "/corrupt.jpg", ResolvedURL: mustParse(ts.URL + "/corrupt.jpg"), Source: scraper.SourceImgSrc},
	}

	summary, err := d.DownloadAll(context.Background(), cands)
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

func mustParse(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(fmt.Sprintf("mustParse failed on %q: %v", s, err))
	}
	return u
}
