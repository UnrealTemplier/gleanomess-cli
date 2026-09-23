package naming

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFilenameFromURL(t *testing.T) {
	tests := []struct {
		url      string
		expected string
	}{
		{
			url:      "https://example.com/photos/cat.jpg?width=400&format=auto",
			expected: "cat.jpg",
		},
		{
			url:      "https://example.com/images/gallery/summer%20beach.png#section",
			expected: "summer beach.png",
		},
		{
			url:      "https://example.com/download/photo",
			expected: "photo",
		},
		{
			url:      "https://example.com/",
			expected: "",
		},
	}

	for _, tt := range tests {
		got := FilenameFromURL(tt.url)
		if got != tt.expected {
			t.Errorf("FilenameFromURL(%q) = %q, expected %q", tt.url, got, tt.expected)
		}
	}
}

func TestParseContentDispositionFilename(t *testing.T) {
	tests := []struct {
		header   string
		expected string
	}{
		{
			header:   `attachment; filename="original_artwork.png"`,
			expected: "original_artwork.png",
		},
		{
			header:   `inline; filename=picture.jpg`,
			expected: "picture.jpg",
		},
		{
			header:   `attachment; filename*=UTF-8''my%20summer%20vacation.jpg`,
			expected: "my summer vacation.jpg",
		},
		{
			header:   ``,
			expected: "",
		},
		{
			header:   `inline`,
			expected: "",
		},
	}

	for _, tt := range tests {
		got := ParseContentDispositionFilename(tt.header)
		if got != tt.expected {
			t.Errorf("ParseContentDispositionFilename(%q) = %q, expected %q", tt.header, got, tt.expected)
		}
	}
}

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{
			input:    "../../etc/passwd.jpg",
			expected: "passwd.jpg",
		},
		{
			input:    `bad:file*name?"<yes>|pipe.png`,
			expected: "bad_file_name___yes__pipe.png",
		},
		{
			input:    "CON.jpg",
			expected: "_CON.jpg",
		},
		{
			input:    "aux.png",
			expected: "_aux.png",
		},
		{
			input:    "nul",
			expected: "_nul",
		},
		{
			input:    "trailing_dots...jpg",
			expected: "trailing_dots.jpg",
		},
		{
			input:    "trailing_spaces   .png",
			expected: "trailing_spaces.png",
		},
	}

	for _, tt := range tests {
		got := SanitizeFilename(tt.input)
		if got != tt.expected {
			t.Errorf("SanitizeFilename(%q) = %q, expected %q", tt.input, got, tt.expected)
		}
	}
}

func TestEnsureExtension(t *testing.T) {
	tests := []struct {
		filename    string
		detectedExt string
		expected    string
	}{
		{
			filename:    "photo",
			detectedExt: ".jpg",
			expected:    "photo.jpg",
		},
		{
			filename:    "photo.jpg",
			detectedExt: ".jpg",
			expected:    "photo.jpg",
		},
		{
			filename:    "photo.jpeg",
			detectedExt: ".jpg",
			expected:    "photo.jpeg",
		},
		{
			filename:    "image.dat",
			detectedExt: ".png",
			expected:    "image.png",
		},
		{
			filename:    "asset.html",
			detectedExt: ".webp",
			expected:    "asset.webp",
		},
	}

	for _, tt := range tests {
		got := EnsureExtension(tt.filename, tt.detectedExt)
		if got != tt.expected {
			t.Errorf("EnsureExtension(%q, %q) = %q, expected %q", tt.filename, tt.detectedExt, got, tt.expected)
		}
	}
}

func TestCollisionAllocation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gleanomess-alloc-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	alloc := NewAllocator(tmpDir)

	// First allocation
	name1 := alloc.Allocate("image.jpg")
	if name1 != "image.jpg" {
		t.Errorf("expected image.jpg, got %q", name1)
	}

	// Create file on disk to simulate existing file
	if err := os.WriteFile(filepath.Join(tmpDir, "image.jpg"), []byte("test"), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Second allocation should be image_2.jpg
	name2 := alloc.Allocate("image.jpg")
	if name2 != "image_2.jpg" {
		t.Errorf("expected image_2.jpg, got %q", name2)
	}

	// Third allocation (even without creating file on disk, allocated map should catch it)
	name3 := alloc.Allocate("image.jpg")
	if name3 != "image_3.jpg" {
		t.Errorf("expected image_3.jpg, got %q", name3)
	}
}
