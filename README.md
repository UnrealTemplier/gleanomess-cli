# GleanoMess

[![CI](https://github.com/UnrealTemplier/gleanomess-cli/actions/workflows/ci.yml/badge.svg)](https://github.com/UnrealTemplier/gleanomess-cli/actions/workflows/ci.yml)
[![License: 0BSD](https://img.shields.io/badge/License-0BSD-blue.svg)](LICENSE)
![Go Version](https://img.shields.io/badge/Go-1.27.1-00ADD8?logo=go)

**GleanoMess** is a focused command-line utility written in pure Go for discovering and downloading raster images from a single web page. It uses pure HTML parsing and heuristics to prefer high-quality original images over thumbnails, downloads assets concurrently with a bounded worker pool, inspects dimensions without decoding full pixel bitmaps into RAM, and deduplicates files without recompression or byte modification.

---

## Status

- **Version:** `0.1.1`
- **Maturity:** Functional utility (v0.1.x). Focused on single-page static HTML scraping; not an enterprise crawler framework or browser automation tool.

---

## Key Features

- **Linux-First & Pure Go:** Built for modern Linux (Fedora x86_64 primary) and fully portable to Windows 11 amd64 and macOS; pure Go CGO-free build with zero external runtime dependencies (no Python, Node.js, or browser engines).
- **Candidate Ranking & Fallback Chains:** Discovers multiple image candidates per logical image slot and ranks them by priority. Ranking defines the attempt order: if a higher-priority candidate fails (HTTP error, corrupted payload, non-raster format, dimension error, or size filter rejection), lower-priority candidates are attempted as fallbacks.
- **No Unnecessary Downloads:** As soon as any candidate in an image slot succeeds and meets size criteria, fallback ceases immediately for that slot.
- **Semantic HTML & Metadata Extraction:** Uses standard `golang.org/x/net/html` for DOM traversal. Extracts images from `<img>`, `<picture>`, enclosing and standalone `<a>` links, lazy-loading `data-*` attributes, OpenGraph and Twitter Cards metadata, validated JSON-LD schemas, and CSS backgrounds.
- **Header-Only Dimension Inspection:** Inspects dimensions via header decoders (`image.DecodeConfig` and box parsers) without decoding full pixel bitmaps into memory.
- **Size Filtering (`--limit-size`):** Enforces `max(width, height) >= N`. If the largest dimension is at least `N` pixels, the image is accepted; otherwise, it is skipped or falls back to remaining candidates.
- **Atomic Concurrency-Safe Deduplication:**
  - **URL Deduplication:** Normalizes URLs so each unique remote URL is fetched at most once across concurrent workers.
  - **Content Deduplication:** Computes streaming SHA-256 hashes during temp file writing. Finalization uses an atomic check-and-save critical section to prevent race conditions when concurrent workers download identical images.
- **Safe Filesystem Handling & Overflow Protection:**
  - Downloads stream to temporary `.gleanomess-*.tmp` files in the output directory.
  - Enforces an internal download safety limit (~500 MiB) with overflow detection (`maxImageBytes + 1`); payloads exceeding the limit are rejected and temp files deleted, preventing truncated files from being finalized.
  - Atomically renames temporary files to their final destinations only after complete verification.
  - Sanitizes filenames for Linux and Windows filesystem safety, guards Windows reserved names (`CON`, `PRN`, `AUX`, `NUL`, etc.), and allocates collision-free filenames (`_2`, `_3`).
- **Resilient HTTP Engine:** Bounded concurrency worker pool (default 4 workers), cookie jar support, redirect limits, referer propagation, and automatic retry with exponential backoff on transient errors (408, 429, 500, 502, 503, 504). Response bodies are closed on all execution paths. Transient failures on individual images do not abort the page job.

---

## Supported Formats

GleanoMess discovers, validates, and downloads only raster formats:

| Format | Magic Bytes / Header Sniffing | Default Extension |
| :--- | :--- | :--- |
| **JPEG** | `0xFF 0xD8 0xFF` / `image/jpeg` | `.jpg` / `.jpeg` |
| **PNG** | `\x89PNG\r\n\x1a\n` / `image/png` | `.png` |
| **GIF** | `GIF87a` / `GIF89a` / `image/gif` | `.gif` |
| **WebP** | `RIFF....WEBP` / `golang.org/x/image/webp` | `.webp` |
| **BMP** | `BM` / `golang.org/x/image/bmp` | `.bmp` |
| **TIFF** | `II*\x00` or `MM\x00*` / `golang.org/x/image/tiff` | `.tiff` |
| **AVIF** | ISOBMFF `ftyp` + `ispe` box header parser | `.avif` |

*Non-raster formats such as SVG (`<svg`, `image/svg+xml`), ICO (`image/x-icon`), and HTML/JSON responses are detected and discarded. Inline base64 `data:` URIs are ignored.*

---

## Requirements & Build

### Requirements
- **Go 1.27.1** or newer.

Pre-compiled standalone binary artifacts (`gleanomess-linux-amd64`, `gleanomess-windows-amd64.exe`, `gleanomess-macos-arm64`) are built automatically on GitHub Actions CI for workflow runs. Public release downloads are not yet published.

### Building from Source

```bash
git clone https://github.com/UnrealTemplier/gleanomess-cli.git
cd gleanomess-cli

# Compile pure Go binary (CGO-free)
CGO_ENABLED=0 go build -o gleanomess ./cmd/gleanomess
```

---

## Usage

### Basic Usage

```bash
gleanomess https://example.com/gallery
```

By default, an output directory named after the webpage hostname (with prefix `www.` removed, e.g. `example.com/`) is created **beside the `gleanomess` executable** (evaluating symlinks).

### Filtering by Size (`--limit-size`)

Enforces that the largest dimension (`max(width, height)`) is at least `N` pixels:

```bash
gleanomess https://example.com/gallery --limit-size 256
```

- `1920x1080` -> Accepted (`max(1920, 1080) = 1920 >= 256`)
- `256x100`   -> Accepted (`max(256, 100) = 256 >= 256`)
- `255x255`   -> Rejected (`max(255, 255) = 255 < 256`)

Images with their largest dimension strictly less than `N` are skipped (or trigger fallback to lower-priority candidates in that slot).

### Custom Output Directory (`--output-dir`)

Overrides the base directory where the site folder is created:

```bash
gleanomess https://example.com/gallery --output-dir /home/user/Downloads
```

This creates `/home/user/Downloads/example.com/`.

### Concurrency Control (`--workers`)

Sets the bounded concurrency worker pool size (default: 4):

```bash
gleanomess https://example.com/gallery --workers 8
```

### Verbose Mode (`--verbose` / `-v`)

Displays detailed diagnostics for each image slot, including source attributes, resolved candidate URLs, HTTP status codes, dimensions, fallback attempts, and skip/duplicate reasons:

```bash
gleanomess https://example.com/gallery --verbose
```

### Version (`--version`)

Displays the installed version and exits:

```bash
gleanomess --version
```

### CLI Options

| Flag | Shorthand | Type | Default | Description |
| :--- | :--- | :--- | :--- | :--- |
| `--limit-size` | | `int` | `0` | Filter images with `max(width, height) >= N` |
| `--output-dir` | | `string` | `""` | Override base output directory |
| `--workers` | | `int` | `4` | Number of concurrent download workers |
| `--verbose` | `-v` | `bool` | `false` | Enable detailed verbose output |
| `--version` | | `bool` | `false` | Show program version and exit |
| `--help` | `-h` | `bool` | `false` | Show usage and flags and exit |

*Note: In standard Go CLI fashion, flags can be passed with either single (`-limit-size`) or double (`--limit-size`) dashes.*

---

## Image Extraction

GleanoMess traverses the static HTML DOM and extracts image candidates from:

1. **Enclosing Anchor Links (`a[href]`):** If an `<a>` tag surrounds an `<img>` and points directly to a raster image (e.g. `<a href="/large.jpg"><img src="/thumb.jpg"></a>`), the link target is prioritized.
2. **High-Resolution Data Attributes:** `data-original`, `data-original-src`, `data-full`, `data-full-src`, `data-fullsize`, `data-large`, `data-large-src`.
3. **Picture Sources (`picture/source`):** Evaluates `<source srcset="..." data-srcset="...">` entries, sorted by highest width descriptor (`w`) or pixel density (`x`).
4. **Image Responsive Sets (`img[srcset]`):** Parses `srcset` and `data-srcset` attributes, sorted by highest resolution.
5. **Lazy-Loading Data Attributes:** `data-src`, `data-image`, `data-image-url`, `data-url`, `data-lazy`, `data-lazy-src`.
6. **Standard Image Source (`img[src]`):** Standard `src` attribute.
7. **Standalone Anchor Links (`a[href]`):** `<a>` links directly pointing to raster images not enclosing an `<img>`.
8. **OpenGraph & Twitter Cards:** `<meta property="og:image">`, `<meta property="og:image:url">`, `<meta name="twitter:image">`, `<meta name="twitter:image:src">`.
9. **JSON-LD Structured Data:** Validated schema properties (`image`, `contentUrl`, `thumbnailUrl`, `ImageObject`, `@graph`). Arbitrary non-image text fields are ignored.
10. **CSS Background Images:** Inline `style="background: url(...)"` attributes and `<style>` blocks.

All relative URLs are resolved against the page URL (or `<base href>` if specified) with fragments (`#...`) stripped.

---

## Candidate Ranking & Fallback Chains

GleanoMess does not guess or synthesize URLs; it selects the **best available candidate exposed by the webpage**.

For each logical image slot, discovered URLs are arranged into a prioritized fallback chain:
1. GleanoMess first attempts to download the highest-ranked candidate (e.g. direct link or original attribute).
2. If that candidate fails (HTTP 4xx/5xx status, network error, non-raster payload, dimension decoding failure, or `--limit-size` rejection), GleanoMess automatically falls back to the next candidate in the slot.
3. As soon as a candidate successfully downloads and passes validation, slot processing finishes immediately. Lower-priority candidates in that slot are not downloaded.

---

## Deduplication

- **URL Deduplication:** A thread-safe URL tracker ensures each unique normalized URL is requested at most once across concurrent workers.
- **Content Deduplication (SHA-256):** During streaming download to temporary storage, a streaming SHA-256 hash is computed. Finalization uses an atomic check-and-save critical section: if another worker has already saved the identical image content, the duplicate file is discarded, reporting a duplicate without race conditions or redundant files on disk.

---

## Download Integrity

- **Original Bytes Preserved:** GleanoMess never recompresses, resizes, or re-encodes downloaded image files.
- **Temporary File Isolation:** Files are written to `.gleanomess-*.tmp` files inside the target directory and only atomically renamed to their final filename upon successful inspection.
- **Truncation & Overflow Safety:** Download responses are read up to the safety limit (~500 MiB) plus one overflow byte (`maxBytes + 1`). If the response exceeds the limit, the download fails, the temporary file is deleted, and truncated data is never finalized as an image.
- **Guaranteed Resource Cleanup:** HTTP response bodies are guaranteed to be closed across all error, redirect, and success branches.

---

## Scope & Limitations

- **No Original Reconstruction:** GleanoMess cannot download an "original" image that the webpage does not expose in its HTML, attributes, CSS, or metadata.
- **Static HTML Only (v0.1.x):** GleanoMess inspects server-rendered HTML. Content generated exclusively via client-side JavaScript execution (SPAs) is outside the scope of v0.1.x unless URLs are present in static markup or JSON-LD.
- **Single Page Only:** GleanoMess downloads assets from the single URL provided. It does not perform multi-page or recursive web crawling.
- **No External CSS Crawling:** External stylesheets referenced via `<link rel="stylesheet">` are not fetched.
- **Data URIs:** Inline base64 `data:` URIs are ignored.
- **No Browser Automation:** No headless browsers, CAPTCHA bypasses, proxy rotators, or authentication session management.

---

## Continuous Integration (CI)

GleanoMess runs automated GitHub Actions CI (`.github/workflows/ci.yml`) on every push and pull request to `main` across three platforms:
- **Linux amd64** (`ubuntu-latest`) — with Go race detector (`-race`)
- **Windows amd64** (`windows-latest`)
- **macOS arm64** (`macos-latest`)

Each CI runner executes:
```bash
go test -v ./...       # with -race on Linux
go vet ./...
go build ./...
```
and uploads standalone binary artifacts (`gleanomess-linux-amd64`, `gleanomess-windows-amd64.exe`, `gleanomess-macos-arm64`) to the workflow run.

---

## Development

Run all tests:
```bash
go test -v ./...
```

Run race detector (Linux):
```bash
go test -race ./...
```

Run static analysis:
```bash
go vet ./...
```

Compile binary:
```bash
go build -o gleanomess ./cmd/gleanomess
```

Format code:
```bash
gofmt -w -s .
```

---

## License

This project is licensed under the **BSD Zero Clause License (0BSD)**. See [LICENSE](LICENSE) for details.
