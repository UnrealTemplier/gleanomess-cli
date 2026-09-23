package imageinfo

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"

	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"
)

func createSolidImage(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 255, G: 0, B: 0, A: 255})
		}
	}
	return img
}

func TestInspectRasterFormats(t *testing.T) {
	tests := []struct {
		name       string
		format     string
		width      int
		height     int
		buildBytes func(w, h int) []byte
	}{
		{
			name:   "PNG",
			format: FormatPNG,
			width:  300,
			height: 200,
			buildBytes: func(w, h int) []byte {
				var buf bytes.Buffer
				_ = png.Encode(&buf, createSolidImage(w, h))
				return buf.Bytes()
			},
		},
		{
			name:   "JPEG",
			format: FormatJPEG,
			width:  400,
			height: 250,
			buildBytes: func(w, h int) []byte {
				var buf bytes.Buffer
				_ = jpeg.Encode(&buf, createSolidImage(w, h), nil)
				return buf.Bytes()
			},
		},
		{
			name:   "GIF",
			format: FormatGIF,
			width:  150,
			height: 150,
			buildBytes: func(w, h int) []byte {
				var buf bytes.Buffer
				_ = gif.Encode(&buf, createSolidImage(w, h), nil)
				return buf.Bytes()
			},
		},
		{
			name:   "BMP",
			format: FormatBMP,
			width:  120,
			height: 80,
			buildBytes: func(w, h int) []byte {
				var buf bytes.Buffer
				_ = bmp.Encode(&buf, createSolidImage(w, h))
				return buf.Bytes()
			},
		},
		{
			name:   "TIFF",
			format: FormatTIFF,
			width:  256,
			height: 128,
			buildBytes: func(w, h int) []byte {
				var buf bytes.Buffer
				_ = tiff.Encode(&buf, createSolidImage(w, h), nil)
				return buf.Bytes()
			},
		},
		{
			name:   "AVIF",
			format: FormatAVIF,
			width:  800,
			height: 600,
			buildBytes: func(w, h int) []byte {
				return makeSyntheticAVIF(uint32(w), uint32(h))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := tt.buildBytes(tt.width, tt.height)
			info, err := Inspect(bytes.NewReader(data), "", "")
			if err != nil {
				t.Fatalf("Inspect failed: %v", err)
			}
			if info.Format != tt.format {
				t.Errorf("expected format %s, got %s", tt.format, info.Format)
			}
			if info.Width != tt.width || info.Height != tt.height {
				t.Errorf("expected %dx%d, got %dx%d", tt.width, tt.height, info.Width, info.Height)
			}
		})
	}
}

func TestInspectNonRasterRejection(t *testing.T) {
	svgData := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="100" height="100"><circle cx="50" cy="50" r="40"/></svg>`)
	_, err := Inspect(bytes.NewReader(svgData), "image/svg+xml", ".svg")
	if !errors.Is(err, ErrNonRasterImage) {
		t.Errorf("expected ErrNonRasterImage for SVG, got %v", err)
	}

	icoData := []byte{0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x10, 0x10}
	_, err = Inspect(bytes.NewReader(icoData), "image/x-icon", ".ico")
	if !errors.Is(err, ErrNonRasterImage) {
		t.Errorf("expected ErrNonRasterImage for ICO, got %v", err)
	}

	htmlData := []byte(`<!DOCTYPE html><html><body><h1>Error</h1></body></html>`)
	_, err = Inspect(bytes.NewReader(htmlData), "text/html", ".html")
	if !errors.Is(err, ErrNonRasterImage) {
		t.Errorf("expected ErrNonRasterImage for HTML, got %v", err)
	}
}
