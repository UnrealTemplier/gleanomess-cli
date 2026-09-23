package naming

import (
	"fmt"
	"mime"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

var windowsReservedNames = map[string]bool{
	"CON":  true,
	"PRN":  true,
	"AUX":  true,
	"NUL":  true,
	"COM1": true,
	"COM2": true,
	"COM3": true,
	"COM4": true,
	"COM5": true,
	"COM6": true,
	"COM7": true,
	"COM8": true,
	"COM9": true,
	"LPT1": true,
	"LPT2": true,
	"LPT3": true,
	"LPT4": true,
	"LPT5": true,
	"LPT6": true,
	"LPT7": true,
	"LPT8": true,
	"LPT9": true,
}

// invalidCharsRegex matches characters forbidden on Linux/Windows filesystems.
// Linux: / and \0
// Windows: \ / : * ? " < > | and control chars 0x00-0x1F
var invalidCharsRegex = regexp.MustCompile(`[\\/:*?"<>|\x00-\x1f]`)

// ParseContentDispositionFilename parses the filename from Content-Disposition header.
func ParseContentDispositionFilename(header string) string {
	if strings.TrimSpace(header) == "" {
		return ""
	}
	_, params, err := mime.ParseMediaType(header)
	if err != nil {
		return ""
	}
	if fn, ok := params["filename*"]; ok && fn != "" {
		// RFC 5987 / RFC 6266 encoding e.g. UTF-8''filename.ext
		parts := strings.SplitN(fn, "''", 2)
		if len(parts) == 2 {
			if unescaped, err := url.PathUnescape(parts[1]); err == nil && unescaped != "" {
				return SanitizeFilename(unescaped)
			}
		}
	}
	if fn, ok := params["filename"]; ok && fn != "" {
		return SanitizeFilename(fn)
	}
	return ""
}

// FilenameFromURL extracts the base filename from a URL path, stripping queries and fragments.
func FilenameFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	base := path.Base(u.Path)
	if base == "" || base == "/" || base == "." {
		return ""
	}
	if unescaped, err := url.PathUnescape(base); err == nil {
		base = unescaped
	}
	return SanitizeFilename(base)
}

// SanitizeFilename cleans the filename for safe storage across Linux and Windows.
func SanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}

	// Strip directory parts
	name = filepath.Base(filepath.ToSlash(name))
	name = path.Base(name)
	if name == "." || name == "/" || name == "\\" {
		return ""
	}

	// Replace forbidden characters with '_'
	name = invalidCharsRegex.ReplaceAllString(name, "_")

	// Separate stem and extension to sanitize trailing spaces/dots on the stem
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)

	// In Windows, stems and extensions cannot end with spaces or dots
	stem = strings.TrimRight(stem, " .")
	ext = strings.TrimRight(ext, " .")

	if stem == "" {
		return ""
	}

	// Check Windows reserved device names (CON, PRN, AUX, NUL, COM1-9, LPT1-9)
	if windowsReservedNames[strings.ToUpper(stem)] {
		stem = "_" + stem
	}

	return stem + ext
}

// EnsureExtension verifies that the filename has a valid extension matching the detected format.
func EnsureExtension(filename, detectedExtension string) string {
	if filename == "" {
		return ""
	}
	detectedExtension = strings.ToLower(detectedExtension)
	if !strings.HasPrefix(detectedExtension, ".") {
		detectedExtension = "." + detectedExtension
	}

	ext := strings.ToLower(filepath.Ext(filename))
	stem := strings.TrimSuffix(filename, ext)

	// If no extension, add detected extension
	if ext == "" || ext == "." {
		return stem + detectedExtension
	}

	// Known compatible extensions for formats
	isCompatible := false
	switch detectedExtension {
	case ".jpg", ".jpeg":
		if ext == ".jpg" || ext == ".jpeg" || ext == ".jpe" || ext == ".jfif" {
			isCompatible = true
		}
	case ".tif", ".tiff":
		if ext == ".tif" || ext == ".tiff" {
			isCompatible = true
		}
	default:
		if ext == detectedExtension {
			isCompatible = true
		}
	}

	if isCompatible {
		return filename
	}

	// If extension does not match actual image format (e.g. .php, .html, or mismatch like .png for a jpeg),
	// replace with the detected format's canonical extension.
	return stem + detectedExtension
}

// ResolveFilename picks the preferred filename according to GleanoMess specifications:
// Content-Disposition -> URL path basename -> generated fallback
func ResolveFilename(contentDisposition, rawURL, detectedExtension, sha256Hex string) string {
	name := ParseContentDispositionFilename(contentDisposition)
	if name == "" {
		name = FilenameFromURL(rawURL)
	}

	if name == "" {
		hashPrefix := sha256Hex
		if len(hashPrefix) > 8 {
			hashPrefix = hashPrefix[:8]
		}
		if hashPrefix == "" {
			hashPrefix = "asset"
		}
		name = fmt.Sprintf("image_%s", hashPrefix)
	}

	name = EnsureExtension(name, detectedExtension)
	name = SanitizeFilename(name)
	if name == "" {
		name = fmt.Sprintf("image_%s%s", sha256Hex[:8], detectedExtension)
	}
	return name
}

// Allocator manages unique filenames in a target directory to prevent collisions
// when multiple concurrent workers download images.
type Allocator struct {
	mu        sync.Mutex
	outputDir string
	allocated map[string]bool
}

// NewAllocator creates a filename allocator for the specified directory.
func NewAllocator(outputDir string) *Allocator {
	return &Allocator{
		outputDir: outputDir,
		allocated: make(map[string]bool),
	}
}

// Allocate returns an unused filename in the output directory, appending _2, _3 if collisions occur.
func (a *Allocator) Allocate(baseFilename string) string {
	a.mu.Lock()
	defer a.mu.Unlock()

	candidate := baseFilename
	ext := filepath.Ext(baseFilename)
	stem := strings.TrimSuffix(baseFilename, ext)

	counter := 1
	for {
		if counter > 1 {
			candidate = fmt.Sprintf("%s_%d%s", stem, counter, ext)
		}

		fullPath := filepath.Join(a.outputDir, candidate)
		// Check both memory registry and filesystem
		if !a.allocated[candidate] {
			if _, err := os.Stat(fullPath); os.IsNotExist(err) {
				a.allocated[candidate] = true
				return candidate
			}
		}
		counter++
	}
}
