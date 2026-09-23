# GleanoMess

**GleanoMess** is a focused, high-performance command-line utility written in Go for discovering and downloading raster images from a single web page. It uses pure HTML parsing and heuristics to prefer high-quality original images over thumbnails, downloads assets concurrently with bounded concurrency, verifies dimensions and integrity, and deduplicates files without recompression or modification.

---

## Features

- **Linux-First & Pure Go:** Built for modern Linux (Fedora x86_64 primary) and fully portable to Windows 11 amd64; CGO-free build with no external runtime dependencies (no Python, Node.js, or browser engines).
- **Candidate Ranking:** Evaluates multiple sources per image element (enclosing `<a>` links, `data-original*` / `data-full*` attributes, `srcset`, `<picture><source>`, etc.) and automatically selects the highest-quality candidate over thumbnails.
- **Robust Extraction:** Uses standard `golang.org/x/net/html` parser for static HTML, inline CSS `url(...)`, `<style>` blocks, OpenGraph/Twitter meta tags, and structured JSON-LD schemas (`@graph`, arrays, ImageObject).
- **Dimension & Format Inspection:** Inspects dimensions via header decoders (`image.DecodeConfig`) without decoding full pixel data into RAM.
- **Size Filter (`--limit-size`):** Discards assets smaller than the threshold using the condition `max(width, height) >= N`.
- **Deduplication:**
  - **URL Deduplication:** Normalizes URLs to ensure every unique remote resource is queried at most once.
  - **Content Deduplication:** Computes streaming SHA-256 hashes during downloads to prevent saving duplicate files under different names.
- **Safe Filesystem Storage:**
  - Files are written to temporary `.tmp` files and only renamed upon successful download, format verification, and size checks.
  - Generates cross-platform clean filenames (handling `Content-Disposition`, URL path basename, Windows forbidden names like `CON`/`PRN`/`NUL`, and collision avoidance `_2`, `_3`).
  - Output directory defaults to `<sanitized_hostname>/` beside the executable, or inside a user-defined directory (`--output-dir`).
- **Resilient HTTP:** Bounded worker pool (default 4 workers), cookie jar support, redirects, configurable timeouts, and automatic retry with exponential backoff on transient errors (408, 429, 500, 502, 503, 504). Errors on individual images do not abort the job.

---

## Supported Formats

GleanoMess supports the following raster image formats:

| Format | File Signatures / Decoders | Extension |
| :--- | :--- | :--- |
| **JPEG** | `0xFF 0xD8 0xFF` / `image/jpeg` | `.jpg` / `.jpeg` |
| **PNG** | `\x89PNG\r\n\x1a\n` / `image/png` | `.png` |
| **GIF** | `GIF87a` / `GIF89a` / `image/gif` | `.gif` |
| **WebP** | `RIFF....WEBP` / `golang.org/x/image/webp` | `.webp` |
| **BMP** | `BM` / `golang.org/x/image/bmp` | `.bmp` |
| **TIFF** | `II*\x00` or `MM\x00*` / `golang.org/x/image/tiff` | `.tiff` |
| **AVIF** | ISOBMFF `ftyp` + `ispe` box header parser (pure Go) | `.avif` |

*Non-raster formats such as SVG (`<svg`, `image/svg+xml`), ICO (`image/x-icon`), and HTML/JSON responses are detected and discarded.*

---

## Installation & Build

### Prerequisites
- Go 1.22 or newer (tested with Go 1.26 on Linux amd64)

### Building from Source

```bash
git clone https://github.com/UnrealTemplier/gleanomess-cli.git
cd gleanomess-cli

# Build binary
go build -o gleanomess ./cmd/gleanomess
```

You can place the compiled `gleanomess` executable in your `PATH` (e.g. `/usr/local/bin` or `~/bin`).

---

## Usage

### Basic Usage

```bash
gleanomess https://example.com/gallery
```

By default, an output directory named after the page hostname (with prefix `www.` removed, e.g. `example.com/`) is created **beside the `gleanomess` executable**.

### Filtering by Size (`--limit-size`)

To download only images where the largest dimension (width or height) is at least `N` pixels:

```bash
gleanomess https://example.com/gallery --limit-size 256
```

- `1920x1080` -> Accepted (max 1920 >= 256)
- `256x100`   -> Accepted (max 256 >= 256)
- `255x255`   -> Rejected (max 255 < 256)

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

Display technical information for each candidate (source tag, original URL, HTTP status, detected format, dimensions, skip/duplicate reasons):

```bash
gleanomess https://example.com/gallery --verbose
```

---

## How Image Candidates Are Discovered & Ranked

GleanoMess does not simply treat `img.src` as the final image. Web galleries frequently embed thumbnails in `<img>` tags while linking to higher-resolution images or declaring them in attributes.

For each image slot, candidates are ranked in descending order of priority:
1. **Direct Anchor Link (`a[href]`):** If an `<a>` tag encloses an `<img>` and points directly to a raster image (e.g. `<a href="/original.jpg"><img src="/thumb.jpg"></a>`), the `/original.jpg` URL is selected.
2. **High-Res Data Attributes:** `data-original`, `data-original-src`, `data-full`, `data-full-src`, `data-fullsize`, `data-large`, `data-large-src`.
3. **Picture Sources (`picture/source`):** Evaluates `<source srcset="..." data-srcset="...">` and selects the candidate with the highest width or density descriptor.
4. **Image Srcset (`img[srcset]`):** Parses `srcset` and `data-srcset`, picking the candidate with the highest width (`1600w > 800w`) or density (`3x > 1x`).
5. **Lazy Loading Attributes:** `data-src`, `data-image`, `data-image-url`, `data-url`, `data-lazy`, `data-lazy-src`.
6. **Standard Image Source:** `img[src]`.

Additionally, GleanoMess independently collects:
- **Standalone `<a>` links** pointing directly to raster images.
- **OpenGraph & Twitter Cards:** `<meta property="og:image">`, `<meta name="twitter:image">`.
- **JSON-LD Structured Data:** `<script type="application/ld+json">` parsing `image`, `contentUrl`, `thumbnailUrl`, `@graph`, and arrays.
- **CSS Backgrounds:** Inline `style="background-image: url(...)"` and `<style>` blocks.

All discovered URLs are normalized, resolved against `<base href>` / page URL, and deduplicated.

---

## Limitations

- **No Original Hallucination:** GleanoMess cannot recreate or download an "original" image that the website does not expose anywhere in its HTML, attributes, or metadata.
- **Static HTML Only (v0.1):** GleanoMess parses the static HTML response returned by the server. If a website generates images strictly via client-side JavaScript execution (SPA frameworks) and does not include URLs in the initial HTML or server-rendered markup, those images are outside the scope of v0.1.
- **Single Page:** GleanoMess processes only the provided URL. It does not perform multi-page or recursive web crawling.
- **No External CSS:** Stylesheets referenced via `<link rel="stylesheet">` are not fetched or parsed in v0.1.
- **Data URIs Ignored:** Inline base64 `data:image/...` URIs are intentionally skipped.

---

## Architecture Overview

```text
               CLI (cmd/gleanomess)
                      │
            HTTP Fetch Webpage
                      │
               HTML Scraper (internal/scraper)
      (golang.org/x/net/html + srcset + JSON-LD + CSS)
                      │
            Candidate Ranking & URL Dedup
                      │
            Worker Pool (internal/downloader)
         ┌────────────┼────────────┐
      Worker 1     Worker 2     Worker N
         │            │            │
         └────────────┼────────────┘
                      │
            Streaming HTTP Download
           (via MultiWriter to Temp File)
                      │
         Content Dedup (Streaming SHA-256)
                      │
      Format & Dimension Sniffing (internal/imageinfo)
        (DecodeConfig: JPEG, PNG, GIF, WebP, BMP, TIFF, AVIF)
                      │
          Size Filter (max(w, h) >= N)
                      │
      Filename Sanitization & Collision (internal/naming)
                      │
       Atomic Rename to Final Filename in Output Dir
```

---

## Development & Testing

Run all tests:
```bash
go test -v ./...
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

## Future Ideas (Post v0.1)

- Optional recursive crawling with bounded depth.
- Optional external CSS stylesheet fetching.
- JSON output mode (`--json`) for scripting integration.
- Configurable User-Agent and custom HTTP headers.
- Rate limiting and domain throttling options.
