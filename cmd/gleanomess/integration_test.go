package main

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
)

func mustParse(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return u
}

func makeJPEG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 100, B: 50, A: 255})
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, nil)
	return buf.Bytes()
}

func makePNG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 50, G: 150, B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func makeGIF(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	_ = gif.Encode(&buf, img, nil)
	return buf.Bytes()
}

func TestEndToEndPipeline(t *testing.T) {
	fullJPEG := makeJPEG(1200, 800)
	thumbJPEG := makeJPEG(150, 100)
	png800 := makePNG(800, 600)
	png400 := makePNG(400, 300)
	tinyGIF := makeGIF(50, 50)
	dupPNG := makePNG(300, 300)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/gallery":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(`
<!DOCTYPE html>
<html>
<body>
    <!-- Enclosing link beats thumb -->
    <a href="/full.jpg">
        <img src="/thumb.jpg">
    </a>

    <!-- Srcset: 800w should beat 400w -->
    <img srcset="/img-400.png 400w, /img-800.png 800w" src="/img-400.png">

    <!-- Tiny image to test limit-size 256 -->
    <img src="/tiny.gif">

    <!-- SVG image to test non-raster rejection -->
    <img src="/vector.svg">

    <!-- Duplicate content under two different URLs -->
    <img src="/dup1.png">
    <img src="/dup2.png">
</body>
</html>
`))
		case "/full.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write(fullJPEG)
		case "/thumb.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write(thumbJPEG)
		case "/img-800.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write(png800)
		case "/img-400.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write(png400)
		case "/tiny.gif":
			w.Header().Set("Content-Type", "image/gif")
			w.Write(tinyGIF)
		case "/vector.svg":
			w.Header().Set("Content-Type", "image/svg+xml")
			w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><circle r="10"/></svg>`))
		case "/dup1.png", "/dup2.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write(dupPNG)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	tmpBaseDir, err := os.MkdirTemp("", "gleanomess-e2e-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpBaseDir)

	args := []string{
		server.URL + "/gallery",
		"--limit-size", "256",
		"--output-dir", tmpBaseDir,
		"--verbose",
	}

	exitCode := run(args)
	if exitCode != 0 {
		t.Fatalf("run returned non-zero exit code: %d", exitCode)
	}

	// Verify target directory was created
	siteDir, err := resolveTargetDir(tmpBaseDir, mustParse(server.URL+"/gallery"))
	if err != nil {
		t.Fatalf("resolveTargetDir failed: %v", err)
	}

	entries, err := os.ReadDir(siteDir)
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}

	fileNames := make(map[string]bool)
	for _, entry := range entries {
		fileNames[entry.Name()] = true
	}

	// 1. full.jpg must exist, thumb.jpg must NOT exist
	if !fileNames["full.jpg"] {
		t.Errorf("expected full.jpg to exist in %v", fileNames)
	}
	if fileNames["thumb.jpg"] {
		t.Errorf("thumb.jpg should have been replaced by full.jpg")
	}

	// 2. img-800.png must exist, img-400.png must NOT exist
	if !fileNames["img-800.png"] {
		t.Errorf("expected img-800.png to exist in %v", fileNames)
	}
	if fileNames["img-400.png"] {
		t.Errorf("img-400.png should have been replaced by img-800.png")
	}

	// 3. tiny.gif (50x50) must NOT exist (filtered out by limit-size 256)
	if fileNames["tiny.gif"] {
		t.Errorf("tiny.gif should have been rejected by --limit-size 256")
	}

	// 4. vector.svg must NOT exist (non-raster)
	if fileNames["vector.svg"] {
		t.Errorf("vector.svg must not be downloaded (non-raster SVG)")
	}

	// 5. Exactly one copy of dup1.png should exist; dup2.png must NOT exist as a separate file
	if !fileNames["dup1.png"] {
		t.Errorf("expected dup1.png to exist")
	}
	if fileNames["dup2.png"] {
		t.Errorf("dup2.png should have been deduplicated by content SHA-256")
	}

	// Total files in directory should be exactly 3: full.jpg, img-800.png, dup1.png
	if len(fileNames) != 3 {
		t.Errorf("expected exactly 3 files, got %d: %v", len(fileNames), fileNames)
	}
}
