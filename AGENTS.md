# AGENTS.md - Instructions for AI Coding Agents

This document defines core principles, architectural invariants, and development rules for future AI coding agents working on **GleanoMess**.

---

## 1. Project Purpose

**GleanoMess** is a focused, standalone command-line utility written in pure Go. Its purpose is to take a single webpage URL, parse the HTML to discover raster images, prefer high-quality/original candidate URLs over thumbnails, download assets concurrently without modifying their bytes, and save them in a dedicated site folder.

---

## 2. Scope

### In Scope for v0.1:
- Linux-first (Fedora x86_64 primary, portable to Windows 11 amd64).
- Pure Go / CGO-free build; standard library + `golang.org/x/net/html` + `golang.org/x/image`.
- Single-page static HTML parsing (no regex for HTML structure).
- Candidate ranking prioritizing original/higher-quality images.
- Parallel downloads using a bounded worker pool (default 4 workers, `--workers N`).
- Size filter `--limit-size N` enforcing `max(width, height) >= N`.
- Strict deduplication (URL normalization + streaming SHA-256 content deduplication).
- Clean filesystem handling: download to temp files, sanitize names, atomic rename.
- Comprehensive automated unit and integration tests.

### Strictly Out of Scope for v0.1:
- Headless browsers (Chromium, Playwright, Selenium) or JS runtimes (Node.js, V8).
- Client-side JavaScript execution.
- Recursive crawling or multi-page traversals.
- Sitemap crawling.
- Browser automation, login, or authentication sessions.
- CAPTCHA solving, proxy rotation, anti-bot bypassing.
- External daemon, background server, or GUI.
- Databases or persistent resume state.
- Range probing or downloading external `<link rel="stylesheet">` CSS files.
- `--dry-run` flag.

If an asset is only generated dynamically via JavaScript execution, it is an accepted limitation of v0.1.

---

## 3. Architecture

The codebase is organized into small, single-purpose packages under `internal/`:

- **`cmd/gleanomess/`**: CLI argument parsing, flags (`--limit-size`, `--output-dir`, `--workers`, `--verbose`), output directory calculation, signal handling, and summary output.
- **`internal/scraper/`**: DOM traversal using `golang.org/x/net/html`. Extracts image slots (`<img>`, `<picture>`, enclosing `<a>`, `srcset`, `data-*`), OpenGraph/Twitter meta tags, JSON-LD schemas, and inline/block CSS. Ranks candidates and resolves URLs relative to `<base href>` / page URL.
- **`internal/imageinfo/`**: Format sniffing (magic bytes and headers) and dimension inspection using `DecodeConfig` (JPEG, PNG, GIF, WebP, BMP, TIFF, AVIF). Rejects non-raster formats (SVG, ICO, HTML).
- **`internal/naming/`**: Resolves final filenames (`Content-Disposition` -> URL path basename -> generated fallback), ensures correct format extension, sanitizes characters for Linux/Windows safety, guards Windows reserved names (`CON`, `PRN`, etc.), and allocates collision-free filenames (`_2`, `_3`).
- **`internal/dedup/`**: Thread-safe URL deduplication and content deduplication via streaming SHA-256 hashes.
- **`internal/downloader/`**: Bounded concurrency worker pool, resilient HTTP client (cookies, redirects, keep-alive), retry with exponential backoff on transient errors (408, 429, 500, 502, 503, 504), temp file streaming, validation pipeline, and progress reporting.

---

## 4. Invariants (DO NOT VIOLATE)

1. **Never recompress downloaded images.** Byte content must be preserved exactly as received over HTTP.
2. **Never trust filename extension alone.** Validate formats via file signatures / decoders with extension as fallback.
3. **Resolve relative URLs against page URL.** Use page URL (or `<base href>` if defined) and strip fragments (`#...`).
4. **`--limit-size` condition is `max(width, height) >= N`.** If the largest dimension is at least `N`, the image is accepted.
5. **Do not fully decode large images only to inspect dimensions.** Always use `DecodeConfig` / box headers without loading pixel bitmaps into RAM.
6. **Download to temporary storage before finalizing.** Write to `.tmp` files; inspect, validate, and hash; atomically rename only upon success; remove temp file on error or rejection.
7. **Deduplicate URLs.** Each normalized URL must be downloaded at most once.
8. **Deduplicate identical content.** Different URLs with identical byte content (matching SHA-256) must not produce duplicate files.
9. **One failed image must not abort the whole job.** Transient or 404 image errors increment the error count but allow other downloads to complete.
10. **Do not replace proper HTML parsing with regex.** Always use `golang.org/x/net/html` for HTML DOM extraction.
11. **Default output is beside executable.** Output directory defaults to `filepath.Dir(exePath)/<sanitized_host>/` (evaluating symlinks).
12. **Do not silently expand v0.1 into a browser/crawler.** Respect the explicit scope boundaries.

---

## 5. Development Rules

- **Use idiomatic Go:** Keep error handling explicit and avoid panic for normal error paths.
- **Run `gofmt`:** Format all code with standard `gofmt -w -s .`.
- **Keep dependencies minimal:** Prefer standard library; new dependencies must be justified and strictly CGO-free.
- **Prefer simple code over abstraction:** Keep structs, functions, and interfaces straightforward.
- **Write tests for important parsing behavior:** Maintain high automated test coverage for URL resolution, srcset parsing, image inspection, size filtering, naming, and HTTP retries.

---

## 6. AI Agent Rules

- **Read `AGENTS.md` before modifying code.**
- **Do not silently enlarge scope.** If a requested feature violates v0.1 scope, explain why or defer it.
- **Do not introduce large frameworks without justification.**
- **Do not rewrite working code for stylistic reasons only.**
- **Run tests after changes:** Always execute `go test ./...` and `go vet ./...`.
- **Update documentation:**
  - Update `README.md` when public CLI behavior or flags change.
  - Update `AGENTS.md` when project invariants or architecture change.

---

## 7. Definition of Done

Any task or PR is considered complete only when:
```bash
go test ./...
go vet ./...
go build ./...
```
All pass with zero errors and zero warnings.
