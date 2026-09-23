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

func makeJPEGColor(w, h int, r, g, b uint8) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: r, G: g, B: b, A: 255})
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, nil)
	return buf.Bytes()
}

func makePNGColor(w, h int, r, g, b uint8) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: r, G: g, B: b, A: 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func makeJPEG(w, h int) []byte {
	return makeJPEGColor(w, h, 200, 100, 50)
}

func makePNG(w, h int) []byte {
	return makePNGColor(w, h, 50, 150, 200)
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

func TestPaginationTraversal(t *testing.T) {
	pngPage1 := makePNGColor(400, 300, 10, 20, 30)
	pngPage2 := makePNGColor(400, 300, 40, 50, 60)
	pngPage3 := makePNGColor(400, 300, 70, 80, 90)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.String() {
		case "/gallery?page=1":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(`
<!DOCTYPE html><html><body>
	<img src="/img-page1.png">
	<a rel="next" href="/gallery?page=2">Next</a>
</body></html>
`))
		case "/gallery?page=2":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(`
<!DOCTYPE html><html><body>
	<img src="/img-page2.png">
	<a rel="next" href="/gallery?page=3">Next</a>
</body></html>
`))
		case "/gallery?page=3":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(`
<!DOCTYPE html><html><body>
	<img src="/img-page3.png">
</body></html>
`))
		case "/img-page1.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write(pngPage1)
		case "/img-page2.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write(pngPage2)
		case "/img-page3.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write(pngPage3)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	// 1. Default: processes only page 1
	t.Run("DefaultSinglePage", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "gleanomess-p1-*")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(tmpDir)

		code := run([]string{server.URL + "/gallery?page=1", "--output-dir", tmpDir})
		if code != 0 {
			t.Fatalf("unexpected exit code %d", code)
		}

		targetDir, _ := resolveTargetDir(tmpDir, mustParse(server.URL+"/gallery?page=1"))
		entries, _ := os.ReadDir(targetDir)
		names := make(map[string]bool)
		for _, e := range entries {
			names[e.Name()] = true
		}

		if !names["img-page1.png"] {
			t.Errorf("expected img-page1.png to exist")
		}
		if names["img-page2.png"] || names["img-page3.png"] {
			t.Errorf("pages 2 and 3 should not be processed in default mode")
		}
	})

	// 2. All pages: processes all 3 pages
	t.Run("AllPages", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "gleanomess-all-*")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(tmpDir)

		code := run([]string{server.URL + "/gallery?page=1", "--all-pages", "--output-dir", tmpDir})
		if code != 0 {
			t.Fatalf("unexpected exit code %d", code)
		}

		targetDir, _ := resolveTargetDir(tmpDir, mustParse(server.URL+"/gallery?page=1"))
		entries, _ := os.ReadDir(targetDir)
		names := make(map[string]bool)
		for _, e := range entries {
			names[e.Name()] = true
		}

		if !names["img-page1.png"] || !names["img-page2.png"] || !names["img-page3.png"] {
			t.Errorf("expected all 3 page images to exist, got %v", names)
		}
	})

	// 3. Max pages limit: --max-pages 2
	t.Run("MaxPagesLimit", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "gleanomess-max-*")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(tmpDir)

		code := run([]string{server.URL + "/gallery?page=1", "--all-pages", "--max-pages", "2", "--output-dir", tmpDir})
		if code != 0 {
			t.Fatalf("unexpected exit code %d", code)
		}

		targetDir, _ := resolveTargetDir(tmpDir, mustParse(server.URL+"/gallery?page=1"))
		entries, _ := os.ReadDir(targetDir)
		names := make(map[string]bool)
		for _, e := range entries {
			names[e.Name()] = true
		}

		if !names["img-page1.png"] || !names["img-page2.png"] {
			t.Errorf("expected page 1 and page 2 images to exist, got %v", names)
		}
		if names["img-page3.png"] {
			t.Errorf("page 3 should not be processed when max-pages=2")
		}
	})

	// 4. Loop protection: page 2 loops back to page 1
	t.Run("LoopProtection", func(t *testing.T) {
		loopServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.String() {
			case "/loop?page=1":
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Write([]byte(`<!DOCTYPE html><html><body><a rel="next" href="/loop?page=2">Next</a></body></html>`))
			case "/loop?page=2":
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Write([]byte(`<!DOCTYPE html><html><body><a rel="next" href="/loop?page=1">Next</a></body></html>`))
			default:
				http.NotFound(w, r)
			}
		}))
		defer loopServer.Close()

		tmpDir, err := os.MkdirTemp("", "gleanomess-loop-*")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(tmpDir)

		code := run([]string{loopServer.URL + "/loop?page=1", "--all-pages", "--output-dir", tmpDir})
		if code != 0 {
			t.Fatalf("unexpected exit code %d", code)
		}
	})
}

func TestLandingResolutionEndToEnd(t *testing.T) {
	fullJPEG1 := makeJPEGColor(1200, 800, 100, 150, 200)
	fullJPEG2 := makeJPEGColor(1200, 800, 200, 150, 100)
	thumbJPEG1 := makeJPEGColor(150, 100, 100, 150, 200)
	thumbJPEG2 := makeJPEGColor(150, 100, 200, 150, 100)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/gallery":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(`
<!DOCTYPE html><html><body>
	<a href="/photo/1001"><img src="/thumb/1001.jpg"></a>
	<a href="/photo/1002"><img src="/thumb/1002.jpg"></a>
</body></html>
`))
		case "/photo/1001":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(`
<!DOCTYPE html><html><body>
	<a href="/full/1001.jpg"><img src="/thumb/1001.jpg"></a>
</body></html>
`))
		case "/photo/1002":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(`
<!DOCTYPE html><html><body>
	<a href="/full/1002.jpg"><img src="/thumb/1002.jpg"></a>
</body></html>
`))
		case "/full/1001.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write(fullJPEG1)
		case "/full/1002.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write(fullJPEG2)
		case "/thumb/1001.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write(thumbJPEG1)
		case "/thumb/1002.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write(thumbJPEG2)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	// 1. Without --resolve-landings and with --limit-size 256:
	// Only thumbnails are on page (150x100), all skipped (< 256)
	t.Run("WithoutResolveLandings", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "gleanomess-noland-*")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(tmpDir)

		code := run([]string{
			server.URL + "/gallery",
			"--limit-size", "256",
			"--output-dir", tmpDir,
		})
		if code != 0 {
			t.Fatalf("unexpected exit code %d", code)
		}

		targetDir, _ := resolveTargetDir(tmpDir, mustParse(server.URL+"/gallery"))
		entries, _ := os.ReadDir(targetDir)
		if len(entries) != 0 {
			t.Errorf("expected 0 files downloaded, got %d", len(entries))
		}
	})

	// 2. With --resolve-landings and --limit-size 256:
	// Landings resolved to 1200x800 originals and downloaded!
	t.Run("WithResolveLandings", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "gleanomess-land-*")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(tmpDir)

		code := run([]string{
			server.URL + "/gallery",
			"--resolve-landings",
			"--limit-size", "256",
			"--output-dir", tmpDir,
		})
		if code != 0 {
			t.Fatalf("unexpected exit code %d", code)
		}

		targetDir, _ := resolveTargetDir(tmpDir, mustParse(server.URL+"/gallery"))
		entries, _ := os.ReadDir(targetDir)
		names := make(map[string]bool)
		for _, e := range entries {
			names[e.Name()] = true
		}

		if !names["1001.jpg"] || !names["1002.jpg"] {
			t.Errorf("expected full originals 1001.jpg and 1002.jpg to be downloaded, got %v", names)
		}
		if len(entries) != 2 {
			t.Errorf("expected exactly 2 files, got %d", len(entries))
		}
	})
}
