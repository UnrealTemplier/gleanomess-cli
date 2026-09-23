package main

import (
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveTargetDir(t *testing.T) {
	tests := []struct {
		url          string
		customBase   string
		expectedHost string
		expectedSlug string
	}{
		{
			url:          "https://www.example.com/gallery",
			customBase:   "/tmp/downloads",
			expectedHost: "example.com",
			expectedSlug: "gallery",
		},
		{
			url:          "https://photos.example.com/album",
			customBase:   "/tmp/downloads",
			expectedHost: "photos.example.com",
			expectedSlug: "album",
		},
		{
			url:          "https://WWW.UPPERCASE.COM/test",
			customBase:   "/tmp/downloads",
			expectedHost: "UPPERCASE.COM",
			expectedSlug: "test",
		},
		{
			url:          "http://example.com:8080/path",
			customBase:   "/tmp/downloads",
			expectedHost: "example.com",
			expectedSlug: "path",
		},
		{
			url:          "https://www.imagefap.com/pictures/9307846/Leonora%20(1501-3000)",
			customBase:   "/tmp/downloads",
			expectedHost: "imagefap.com",
			expectedSlug: "pictures-9307846-leonora-1501-3000",
		},
		{
			url:          "https://example.com/",
			customBase:   "/tmp/downloads",
			expectedHost: "example.com",
			expectedSlug: "root",
		},
	}

	for _, tt := range tests {
		u, err := url.Parse(tt.url)
		if err != nil {
			t.Fatalf("Parse(%q) failed: %v", tt.url, err)
		}
		target, err := resolveTargetDir(tt.customBase, u)
		if err != nil {
			t.Fatalf("resolveTargetDir failed: %v", err)
		}

		// Verify host folder
		expectedParent := filepath.Join(tt.customBase, tt.expectedHost)
		if filepath.Dir(target) != expectedParent {
			t.Errorf("expected parent directory %q, got %q", expectedParent, filepath.Dir(target))
		}

		// Verify page folder prefix and length
		pageFolder := filepath.Base(target)
		if !strings.HasPrefix(pageFolder, tt.expectedSlug+"-") {
			t.Errorf("expected page folder to start with %q, got %q", tt.expectedSlug+"-", pageFolder)
		}
	}
}

func TestResolveTargetDir_Partitioning(t *testing.T) {
	customBase := "/tmp/downloads"

	// 1. Two different paths on the same hostname must resolve to DIFFERENT directories under the same host folder
	u1, _ := url.Parse("https://example.com/gallery/one")
	u2, _ := url.Parse("https://example.com/gallery/two")

	dir1, err1 := resolveTargetDir(customBase, u1)
	dir2, err2 := resolveTargetDir(customBase, u2)
	if err1 != nil || err2 != nil {
		t.Fatalf("unexpected error resolving dirs: %v, %v", err1, err2)
	}

	if dir1 == dir2 {
		t.Errorf("expected different directories for %q and %q, got same: %s", u1, u2, dir1)
	}

	if filepath.Dir(dir1) != filepath.Dir(dir2) {
		t.Errorf("expected same host parent directory for both, got %s vs %s", filepath.Dir(dir1), filepath.Dir(dir2))
	}

	if !strings.HasPrefix(filepath.Base(dir1), "gallery-one-") {
		t.Errorf("expected dir1 base to start with 'gallery-one-', got %s", filepath.Base(dir1))
	}
	if !strings.HasPrefix(filepath.Base(dir2), "gallery-two-") {
		t.Errorf("expected dir2 base to start with 'gallery-two-', got %s", filepath.Base(dir2))
	}

	// 2. Same path with different query parameters must resolve to DIFFERENT directories
	u3, _ := url.Parse("https://example.com/gallery?id=1")
	u4, _ := url.Parse("https://example.com/gallery?id=2")

	dir3, err3 := resolveTargetDir(customBase, u3)
	dir4, err4 := resolveTargetDir(customBase, u4)
	if err3 != nil || err4 != nil {
		t.Fatalf("unexpected error resolving query dirs: %v, %v", err3, err4)
	}

	if dir3 == dir4 {
		t.Errorf("expected different directories for %q and %q, got same: %s", u3, u4, dir3)
	}

	// 3. Determinism: identical URL must always resolve to the exact same directory
	dir1Again, err := resolveTargetDir(customBase, u1)
	if err != nil {
		t.Fatalf("unexpected error on repeated resolveTargetDir: %v", err)
	}
	if dir1 != dir1Again {
		t.Errorf("expected deterministic directory, got %s vs %s", dir1, dir1Again)
	}
}

func TestCLIVerifications(t *testing.T) {
	// Missing URL -> exit code 2
	if code := run([]string{}); code != 2 {
		t.Errorf("expected exit code 2 for missing args, got %d", code)
	}

	// Multiple URLs -> exit code 2
	if code := run([]string{"https://example.com", "https://other.com"}); code != 2 {
		t.Errorf("expected exit code 2 for multiple URLs, got %d", code)
	}

	// Invalid URL scheme -> exit code 2
	if code := run([]string{"ftp://example.com"}); code != 2 {
		t.Errorf("expected exit code 2 for invalid scheme, got %d", code)
	}

	// Negative limit size -> exit code 2
	if code := run([]string{"--limit-size", "-10", "https://example.com"}); code != 2 {
		t.Errorf("expected exit code 2 for negative limit-size, got %d", code)
	}

	// Invalid workers -> exit code 2
	if code := run([]string{"--workers", "0", "https://example.com"}); code != 2 {
		t.Errorf("expected exit code 2 for workers=0, got %d", code)
	}

	// Version flag -> exit code 0
	if code := run([]string{"--version"}); code != 0 {
		t.Errorf("expected exit code 0 for --version, got %d", code)
	}

	// Help flag -> exit code 0
	if code := run([]string{"--help"}); code != 0 {
		t.Errorf("expected exit code 0 for --help, got %d", code)
	}
}
