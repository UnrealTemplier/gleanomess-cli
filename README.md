# GleanoMess

[![CI](https://github.com/UnrealTemplier/gleanomess-cli/actions/workflows/ci.yml/badge.svg)](https://github.com/UnrealTemplier/gleanomess-cli/actions/workflows/ci.yml)
[![License: 0BSD](https://img.shields.io/badge/License-0BSD-blue.svg)](LICENSE)
![Go Version](https://img.shields.io/badge/Go-1.27.1-00ADD8?logo=go)

**GleanoMess** is a focused, high-performance command-line utility written in pure Go for discovering and downloading raster images from a single web page. It uses pure HTML parsing and heuristics to prefer high-quality original images over thumbnails, downloads assets concurrently with a bounded worker pool, inspects dimensions without decoding full pixel bitmaps into RAM, and deduplicates files without recompression or byte modification.

---

## Key Features

- **Linux-First & Pure Go:** Built for modern Linux (Fedora x86_64 primary) and fully portable to Windows 11 amd64 and macOS; pure Go CGO-free build with zero external runtime dependencies (no Python, Node.js, or browser engines).
- **Candidate Ranking & Fallback Chains:** Evaluates multiple sources per visual image slot (enclosing `<a>` links, `data-original*` / `data-full*` attributes, `<picture><source>`, `srcset`, `data-src`, `src`). Ranking determines the order of download attempts. If a higher-priority candidate fails (HTTP 4xx/5xx, network error, non-raster format, dimension decoding failure, or size filter rejection), lower-priority candidates in the slot are tried as fallbacks.
- **No Unnecessary Downloads:** As soon as a candidate in an image slot succeeds and meets size requirements, fallback ceases immediately.
- **Robust Semantic Extraction:** Uses standard `golang.org/x/net/html` parser for static HTML, inline CSS `url(...)`, `<style>` blocks, OpenGraph/Twitter meta tags, and structured JSON-LD schemas (`@graph`, arrays, ImageObject). Arbitrary JSON-LD strings (such as headlines, `@context`, or descriptions) are never converted into image candidates.
- **Dimension & Format Inspection:** Inspects dimensions via header decoders (`image.DecodeConfig`) without loading full pixel bitmaps into RAM.
- **Size Filter (`--limit-size`):** Enforces the condition `max(width, height) >= N`. If the largest dimension is at least `N` pixels, the image is accepted; otherwise, it is skipped (or falls back to other slot candidates).
- **Atomic Deduplication:**
  - **URL Deduplication:** Normalizes URLs to ensure each unique remote URL is queried at most once across concurrent workers.
  - **Content Deduplication:** Computes streaming SHA-256 hashes during temp file writing. Finalization uses atomic check-and-save semantics to ensure that concurrent workers processing identical content produce exactly one final file, with automatic state rollback on failure.
- **Safe Filesystem Storage & Overflow Protection:**
  - Enforces internal download size safety limit (~500 MiB) with overflow detection, guaranteeing that truncated files are deleted and never finalized.
  - Files are written to temporary `.tmp` files in the output directory and atomically renamed only upon full verification.
  - Sanitizes filenames for Linux and Windows safety (guarding reserved names `CON`, `PRN`, `AUX`, `NUL`, `COM1-9`, `LPT1-9`, and allocating collision-free names `_2`, `_3`).
  - Output directory defaults to `<sanitized_hostname>/` beside the executable, or inside a user-specified path (`--output-dir`).
- **Resilient HTTP Engine:** Bounded worker pool (default 4 workers), cookie jar support, redirect limits, referer propagation, and automatic retry with exponential backoff on transient errors (408, 429, 500, 502, 503, 504). Response bodies are reliably closed on every execution and error path. Errors on individual images never abort the entire job.

---

## Supported Formats

GleanoMess discovers, validates, and preserves the following raster formats:

| Format | Magic Bytes / Sniffing | Extension |
| :--- | :--- | :--- |
| **JPEG** | `0xFF 0xD8 0xFF` / `image/jpeg` | `.jpg` / `.jpeg` |
| **PNG** | `\x89PNG\r\n\x1a\n` / `image/png` | `.png` |
| **GIF** | `GIF87a` / `GIF89a` / `image/gif` | `.gif` |
| **WebP** | `RIFF....WEBP` / `golang.org/x/image/webp` | `.webp` |
| **BMP** | `BM` / `golang.org/x/image/bmp` | `.bmp` |
| **TIFF** | `II*\x00` or `MM\x00*` / `golang.org/x/image/tiff` | `.tiff` |
| **AVIF** | ISOBMFF `ftyp` + `ispe` box header parser | `.avif` |

*Non-raster formats such as SVG (`<svg`, `image/svg+xml`), ICO (`image/x-icon`), and HTML/JSON responses are detected and discarded.*

---

## Installation & Build

### Prerequisites
- **Go 1.27.1** or newer.

### Building from Source

```bash
git clone https://github.com/UnrealTemplier/gleanomess-cli.git
cd gleanomess-cli

# Compile pure Go binary
CGO_ENABLED=0 go build -o gleanomess ./cmd/gleanomess
```

You can place the compiled `gleanomess` executable in your `PATH` (e.g. `/usr/local/bin` or `~/bin`).

---

## Usage

### Basic Usage

```bash
gleanomess https://example.com/gallery
```

By default, an output directory named after the page hostname (with prefix `www.` removed, e.g. `example.com/`) is created **beside the `gleanomess` executable** (evaluating symlinks).

### Filtering by Size (`--limit-size`)

To download only images where the largest dimension (width or height) is at least `N` pixels:

```bash
gleanomess https://example.com/gallery --limit-size 256
```

- `1920x1080` -> Accepted (`max(1920, 1080) >= 256`)
- `256x100`   -> Accepted (`max(256, 100) >= 256`)
- `255x255`   -> Rejected (`max(255, 255) < 256`)

### Custom Output Directory (`--output-dir`)

To override the base output directory:

```bash
gleanomess https://example.com/gallery --output-dir /home/user/Downloads
```

This creates `/home/user/Downloads/example.com/`.

### Concurrency Control (`--workers`)

Specify the number of concurrent download workers (default: 4):

```bash
gleanomess https://example.com/gallery --workers 8
```

### Verbose Mode (`--verbose` / `-v`)

Display detailed diagnostics for each image slot (source tag, candidate URLs, HTTP status, detected format, dimensions, fallbacks, and skip/duplicate reasons):

```bash
gleanomess https://example.com/gallery --verbose
```

### Version (`--version`)

Display the installed version and exit:

```bash
gleanomess --version
```

---

## Candidate Ranking & Fallback Chains

GleanoMess tries to obtain the highest-quality image candidate exposed by the webpage. Ranking determines the **order of attempts**, rather than permanently committing to a single URL.

For each visual image slot, candidates are prioritized in descending order:
1. **Direct Anchor Link (`a[href]`):** If an `<a>` tag encloses an `<img>` and points directly to a raster image (e.g. `<a href="/original.jpg"><img src="/thumb.jpg"></a>`), `/original.jpg` is attempted first.
2. **High-Res Data Attributes:** `data-original`, `data-original-src`, `data-full`, `data-full-src`, `data-fullsize`, `data-large`, `data-large-src`.
3. **Picture Sources (`picture/source`):** Evaluates `<source srcset="..." data-srcset="...">` ranked by largest width descriptor or pixel density.
4. **Image Srcset (`img[srcset]`):** Parses `srcset` and `data-srcset` ranked by resolution (`1600w > 800w`).
5. **Lazy Loading Attributes:** `data-src`, `data-image`, `data-image-url`, `data-url`, `data-lazy`, `data-lazy-src`.
6. **Standard Image Source:** `img[src]`.

If a higher-priority candidate fails (e.g. HTTP 404/403, corrupted file, non-raster payload, or size below `--limit-size`), GleanoMess automatically falls back to try the next candidate in the slot. Once a candidate succeeds, the slot is complete and lower candidates are not downloaded.

Additionally, GleanoMess extracts:
- **Standalone `<a>` links** pointing directly to raster images.
- **OpenGraph & Twitter Cards:** `<meta property="og:image">`, `<meta name="twitter:image">`.
- **JSON-LD Structured Data:** Validated fields (`image`, `contentUrl`, `thumbnailUrl`, `ImageObject`, `@graph`).
- **CSS Backgrounds:** Inline `style="background-image: url(...)"` and `<style>` blocks.

---

## Scope & Limitations

- **No Original Reconstruction:** GleanoMess cannot recreate or download an "original" image that the website does not expose in its HTML, attributes, or metadata.
- **Static HTML Only (v0.1.x):** GleanoMess inspects server-rendered HTML. Resources generated exclusively via client-side JavaScript execution (SPAs) are outside the scope of v0.1.x unless their URLs are present in accessible DOM attributes or JSON-LD data.
- **Single Page:** GleanoMess downloads assets from the single URL provided. It does not perform multi-page or recursive web crawling.
- **No External CSS Crawling:** External stylesheets referenced via `<link rel="stylesheet">` are not fetched.
- **Data URIs:** Inline base64 `data:image/...` URIs are ignored.
- **No Browser Automation:** No headless browsers, CAPTCHA bypasses, or proxy rotators.

---

## Continuous Integration (CI)

GleanoMess includes automated GitHub Actions CI testing on every push and pull request across three operating systems:
- **Linux** (`ubuntu-latest`, amd64) — with `-race` detector enabled
- **Windows** (`windows-latest`, amd64)
- **macOS** (`macos-latest`, arm64)

Every CI run executes:
```bash
go test ./...
go vet ./...
go build ./...
```
and produces downloadable standalone executable artifacts (`gleanomess-linux-amd64`, `gleanomess-windows-amd64.exe`, `gleanomess-macos-arm64`).

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

Format code:
```bash
gofmt -w -s .
```

---

## License

This project is licensed under the **BSD Zero Clause License (0BSD)**. See [LICENSE](LICENSE) for details.
