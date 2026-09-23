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
		expectedTail string
	}{
		{
			url:          "https://www.example.com/gallery",
			customBase:   "/tmp/downloads",
			expectedTail: filepath.Join("/tmp/downloads", "example.com"),
		},
		{
			url:          "https://photos.example.com/album",
			customBase:   "/tmp/downloads",
			expectedTail: filepath.Join("/tmp/downloads", "photos.example.com"),
		},
		{
			url:          "https://WWW.UPPERCASE.COM/test",
			customBase:   "/tmp/downloads",
			expectedTail: filepath.Join("/tmp/downloads", "UPPERCASE.COM"),
		},
		{
			url:          "http://example.com:8080/path",
			customBase:   "/tmp/downloads",
			expectedTail: filepath.Join("/tmp/downloads", "example.com"),
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
		if !strings.HasSuffix(target, tt.expectedTail) {
			t.Errorf("expected target to end with %q, got %q", tt.expectedTail, target)
		}
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
}
