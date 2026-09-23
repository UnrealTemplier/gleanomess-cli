package imageinfo

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"mime"
	"path/filepath"
	"strings"

	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"
	"golang.org/x/image/webp"
)

var (
	ErrNonRasterImage    = errors.New("image format is non-raster (e.g. SVG, ICO)")
	ErrUnsupportedFormat = errors.New("unsupported image format")
	ErrInvalidDimensions = errors.New("invalid image dimensions")
)

// Supported raster format names
const (
	FormatJPEG = "jpeg"
	FormatPNG  = "png"
	FormatGIF  = "gif"
	FormatWebP = "webp"
	FormatBMP  = "bmp"
	FormatTIFF = "tiff"
	FormatAVIF = "avif"
)

type ImageInfo struct {
	Format    string // e.g. "jpeg", "png", "webp", "avif"
	Width     int
	Height    int
	MimeType  string
	Extension string // e.g. ".jpg", ".png", ".webp", ".avif"
}

// SniffFormat analyzes the header bytes, content-type, and extension
// to determine if the data represents a supported raster format.
func SniffFormat(header []byte, contentType, ext string) (format string, err error) {
	// First check for non-raster formats to reject them immediately
	if isSVG(header, contentType, ext) {
		return "", fmt.Errorf("%w: SVG", ErrNonRasterImage)
	}
	if isICO(header, contentType, ext) {
		return "", fmt.Errorf("%w: ICO", ErrNonRasterImage)
	}
	if isHTML(header, contentType) {
		return "", fmt.Errorf("%w: HTML text response", ErrNonRasterImage)
	}

	// 1. Check magic signatures
	if isJPEG(header) {
		return FormatJPEG, nil
	}
	if isPNG(header) {
		return FormatPNG, nil
	}
	if isGIF(header) {
		return FormatGIF, nil
	}
	if isWebP(header) {
		return FormatWebP, nil
	}
	if isBMP(header) {
		return FormatBMP, nil
	}
	if isTIFF(header) {
		return FormatTIFF, nil
	}
	if IsAVIF(header) {
		return FormatAVIF, nil
	}

	// 2. Fall back to Content-Type if signature was ambiguous
	ctFormat := formatFromContentType(contentType)
	if ctFormat != "" {
		return ctFormat, nil
	}

	// 3. Fall back to extension
	extFormat := formatFromExtension(ext)
	if extFormat != "" {
		return extFormat, nil
	}

	return "", ErrUnsupportedFormat
}

// Inspect reads the image header from an io.ReadSeeker, validates it as a raster format,
// and extracts the dimensions without decoding the whole image into RAM.
func Inspect(r io.ReadSeeker, contentType, ext string) (*ImageInfo, error) {
	header := make([]byte, 512)
	n, err := r.Read(header)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("reading image header: %w", err)
	}
	header = header[:n]

	format, err := SniffFormat(header, contentType, ext)
	if err != nil {
		return nil, err
	}

	// Rewind to beginning of stream for decoding config
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewinding image stream: %w", err)
	}

	var cfg image.Config
	switch format {
	case FormatJPEG:
		cfg, err = jpeg.DecodeConfig(r)
	case FormatPNG:
		cfg, err = png.DecodeConfig(r)
	case FormatGIF:
		cfg, _, err = image.DecodeConfig(r)
	case FormatWebP:
		cfg, err = webp.DecodeConfig(r)
	case FormatBMP:
		cfg, err = bmp.DecodeConfig(r)
	case FormatTIFF:
		cfg, err = tiff.DecodeConfig(r)
	case FormatAVIF:
		cfg, err = DecodeAVIFConfig(r)
	default:
		cfg, _, err = image.DecodeConfig(r)
	}

	if err != nil {
		return nil, fmt.Errorf("decoding image config for %s: %w", format, err)
	}

	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, fmt.Errorf("%w: %dx%d", ErrInvalidDimensions, cfg.Width, cfg.Height)
	}

	info := &ImageInfo{
		Format:    format,
		Width:     cfg.Width,
		Height:    cfg.Height,
		MimeType:  CanonicalMIME(format),
		Extension: CanonicalExtension(format),
	}
	return info, nil
}

// CanonicalExtension returns the canonical file extension with dot for a format.
func CanonicalExtension(format string) string {
	switch format {
	case FormatJPEG:
		return ".jpg"
	case FormatPNG:
		return ".png"
	case FormatGIF:
		return ".gif"
	case FormatWebP:
		return ".webp"
	case FormatBMP:
		return ".bmp"
	case FormatTIFF:
		return ".tiff"
	case FormatAVIF:
		return ".avif"
	default:
		return ""
	}
}

// CanonicalMIME returns the MIME type for a format.
func CanonicalMIME(format string) string {
	switch format {
	case FormatJPEG:
		return "image/jpeg"
	case FormatPNG:
		return "image/png"
	case FormatGIF:
		return "image/gif"
	case FormatWebP:
		return "image/webp"
	case FormatBMP:
		return "image/bmp"
	case FormatTIFF:
		return "image/tiff"
	case FormatAVIF:
		return "image/avif"
	default:
		return "application/octet-stream"
	}
}

// IsRasterExtension checks if the extension is recognized as one of the raster formats.
func IsRasterExtension(ext string) bool {
	ext = strings.ToLower(strings.TrimPrefix(ext, "."))
	switch ext {
	case "jpg", "jpeg", "png", "gif", "webp", "bmp", "tif", "tiff", "avif":
		return true
	default:
		return false
	}
}

func isJPEG(b []byte) bool {
	return len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF
}

func isPNG(b []byte) bool {
	return len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n"
}

func isGIF(b []byte) bool {
	return len(b) >= 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a")
}

func isWebP(b []byte) bool {
	return len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP"
}

func isBMP(b []byte) bool {
	return len(b) >= 2 && b[0] == 'B' && b[1] == 'M'
}

func isTIFF(b []byte) bool {
	if len(b) < 4 {
		return false
	}
	// Little-endian "II*\x00" or big-endian "MM\x00*"
	return (b[0] == 0x49 && b[1] == 0x49 && b[2] == 0x2A && b[3] == 0x00) ||
		(b[0] == 0x4D && b[1] == 0x4D && b[2] == 0x00 && b[3] == 0x2A)
}

func isSVG(header []byte, contentType, ext string) bool {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if mediaType == "image/svg+xml" {
		return true
	}
	ext = strings.ToLower(strings.TrimPrefix(ext, "."))
	if ext == "svg" || ext == "svgz" {
		return true
	}
	s := strings.ToLower(string(header))
	return strings.Contains(s, "<svg") || (strings.Contains(s, "<?xml") && strings.Contains(s, "<svg"))
}

func isICO(header []byte, contentType, ext string) bool {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if mediaType == "image/x-icon" || mediaType == "image/vnd.microsoft.icon" {
		return true
	}
	ext = strings.ToLower(strings.TrimPrefix(ext, "."))
	if ext == "ico" {
		return true
	}
	// ICO magic: 0x00 0x00 0x01 0x00
	return len(header) >= 4 && header[0] == 0x00 && header[1] == 0x00 && header[2] == 0x01 && header[3] == 0x00
}

func isHTML(header []byte, contentType string) bool {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if mediaType == "text/html" || mediaType == "application/xhtml+xml" {
		return true
	}
	trimmed := bytes.TrimSpace(header)
	s := strings.ToLower(string(trimmed))
	return strings.HasPrefix(s, "<!doctype html") || strings.HasPrefix(s, "<html")
}

func formatFromContentType(contentType string) string {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	switch mediaType {
	case "image/jpeg", "image/jpg":
		return FormatJPEG
	case "image/png":
		return FormatPNG
	case "image/gif":
		return FormatGIF
	case "image/webp":
		return FormatWebP
	case "image/bmp", "image/x-ms-bmp":
		return FormatBMP
	case "image/tiff":
		return FormatTIFF
	case "image/avif":
		return FormatAVIF
	default:
		return ""
	}
}

func formatFromExtension(ext string) string {
	ext = strings.ToLower(filepath.Ext(ext))
	if ext == "" {
		return ""
	}
	switch ext {
	case ".jpg", ".jpeg":
		return FormatJPEG
	case ".png":
		return FormatPNG
	case ".gif":
		return FormatGIF
	case ".webp":
		return FormatWebP
	case ".bmp":
		return FormatBMP
	case ".tif", ".tiff":
		return FormatTIFF
	case ".avif":
		return FormatAVIF
	default:
		return ""
	}
}
