# AGENTS.md - Instructions for AI Coding Agents

This document defines core principles, architectural invariants, and development rules for future AI coding agents working on **GleanoMess**.

> **Note for AI Agents:**
> `README.md` describes the **user-facing behavior, CLI flags, and public interface**.
> `AGENTS.md` governs **internal engineering invariants, design decisions, and development rules**.

---

## 1. Project Purpose

**GleanoMess** is a focused, standalone command-line utility written in pure Go. Its purpose is to take a single webpage URL, parse the HTML to discover raster images, prefer high-quality/original candidate URLs over thumbnails, download assets concurrently without modifying their bytes, and save them in a dedicated site folder.

---

## 2. Scope

### In Scope for v0.1.x:
- Linux-first (Fedora x86_64 primary, portable to Windows 11 amd64 and macOS).
- Pure Go / CGO-free build; standard library + `golang.org/x/net/html` + `golang.org/x/image`.
- Single-page static HTML parsing (no regex for HTML structure).
- Candidate ranking prioritizing original/higher-quality images with ordered fallback chains per image slot.
- Parallel downloads using a bounded worker pool (default 4 workers, `--workers N`).
- Size filter `--limit-size N` enforcing `max(width, height) >= N`.
- Strict atomic deduplication (thread-safe URL normalization + streaming SHA-256 content deduplication atomic with finalization).
- Clean filesystem handling: download to temp files, sanitize names, atomic rename, overflow/truncation safety limits.
- Comprehensive automated unit and integration tests.

### Strictly Out of Scope for v0.1.x:
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

If an asset is only generated dynamically via JavaScript execution, it is an accepted limitation of v0.1.x.

---

## 3. Architecture

The codebase is organized into small, single-purpose packages under `internal/`:

- **`cmd/gleanomess/`**: CLI argument parsing, flags (`--limit-size`, `--output-dir`, `--workers`, `--verbose`, `--version`), output directory calculation, signal handling, and summary output.
- **`internal/scraper/`**: DOM traversal using `golang.org/x/net/html`. Extracts image slots (`<img>`, `<picture>`, enclosing `<a>`, `srcset`, `data-*`), OpenGraph/Twitter meta tags, JSON-LD schemas, and inline/block CSS. Ranks candidates and resolves URLs relative to `<base href>` / page URL.
- **`internal/imageinfo/`**: Format sniffing (magic bytes and headers) and dimension inspection using `DecodeConfig` (JPEG, PNG, GIF, WebP, BMP, TIFF, AVIF). Rejects non-raster formats (SVG, ICO, HTML).
- **`internal/naming/`**: Resolves final filenames (`Content-Disposition` -> URL path basename -> generated fallback), ensures correct format extension, sanitizes characters for Linux/Windows safety, guards Windows reserved names (`CON`, `PRN`, etc.), and allocates collision-free filenames (`_2`, `_3`).
- **`internal/dedup/`**: Thread-safe URL deduplication and atomic content deduplication via streaming SHA-256 hashes and serialized finalization.
- **`internal/downloader/`**: Bounded concurrency worker pool, resilient HTTP client (cookies, redirects, keep-alive), retry with exponential backoff on transient errors (408, 429, 500, 502, 503, 504), temp file streaming, overflow detection, validation pipeline, and progress reporting.

---

## 4. Invariants (DO NOT VIOLATE)

1. **Never recompress downloaded images.** Byte content must be preserved exactly as received over HTTP.
2. **Never trust filename extension alone.** Validate formats via file signatures / decoders with extension as fallback.
3. **Resolve relative URLs against page URL.** Use page URL (or `<base href>` if defined) and strip fragments (`#...`).
4. **`--limit-size` means `max(width, height) >= N`.** If the largest dimension is at least `N`, the image is accepted.
5. **Do not fully decode large images only to inspect dimensions.** Always use `DecodeConfig` / box headers without loading pixel bitmaps into RAM.
6. **Download to temporary storage before finalizing.** Write to `.tmp` files; inspect, validate, and hash; atomically rename only upon success; remove temp file on error or rejection.
7. **URL deduplication must be concurrency-safe.** Each normalized URL must be downloaded at most once across concurrent workers.
8. **Content deduplication must be atomic with finalization.** Checking hashes, allocating filenames, and renaming must be atomic to prevent concurrent workers from saving duplicate files. If rename/storage fails, the tracker state must rollback and not claim success.
9. **Ranked candidates form an ordered fallback chain.** For any logical image slot, ranking specifies the attempt order. If a higher-priority candidate fails (HTTP error, corrupted data, non-raster format, or limit-size rejection), lower-priority candidates in that slot must be attempted.
10. **Do not download all candidates unnecessarily.** As soon as a candidate in an image slot succeeds and meets size criteria, stop processing that slot immediately.
11. **JSON-LD must only extract semantically relevant image fields.** Extract only valid image properties (`image`, `contentUrl`, `thumbnailUrl`, `ImageObject`, `@graph`, arrays). Never treat arbitrary strings (e.g. `@context`, `headline`, `author`, `name`) as image candidates.
12. **A failed image must not abort the whole page job.** Transient or 404 image errors increment the error count but allow other image slots to complete.
13. **Never silently accept truncated downloads.** When response payloads exceed the internal download safety limit (~500 MiB), detect overflow (`maxImageBytes + 1`), fail the download, delete the temporary file, and never finalize truncated data as an image.
14. **HTTP response bodies must be closed on every ownership path.** Every `Response.Body` must be guaranteed to close, including permanent errors (400, 401, 403, 404), redirect errors, retry exhaustion, and intermediate status checks.
15. **Default output is beside executable.** Output directory defaults to `filepath.Dir(exePath)/<sanitized_host>/` (evaluating symlinks).
16. **Do not silently expand v0.1.x into a browser/crawler framework.** Respect explicit scope boundaries.

---

## 5. Toolchain

- **Go 1.27.1** (minimum required toolchain across `go.mod`, CI, and development environments).
- Standard library + pure Go dependencies (`golang.org/x/net`, `golang.org/x/image`).
- Strictly **CGO-free** (`CGO_ENABLED=0`).

---

## 6. Continuous Integration (CI)

Automated GitHub Actions CI (`.github/workflows/ci.yml`) runs on every push and pull request across the matrix:
- **Linux amd64** (`ubuntu-latest`) — with `go test -v -race ./...`
- **Windows amd64** (`windows-latest`) — with `go test -v ./...`
- **macOS arm64** (`macos-latest`) — with `go test -v ./...`

Each OS runner must pass the mandatory quality gates:
```bash
go test ./...
go vet ./...
go build ./...
```
CI builds standalone binary artifacts (`gleanomess-linux-amd64`, `gleanomess-windows-amd64.exe`, `gleanomess-macos-arm64`) uploaded to the workflow run without publishing public releases.

---

## 7. Development Rules

- **Use idiomatic Go:** Keep error handling explicit and avoid panic for normal error paths.
- **Run `gofmt`:** Format all code with standard `gofmt -w -s .`.
- **Keep dependencies minimal:** Prefer standard library; new dependencies must be justified and strictly CGO-free.
- **Prefer simple code over abstraction:** Keep structs, functions, and interfaces straightforward.
- **Write tests for important parsing behavior:** Maintain high automated test coverage for URL resolution, srcset parsing, image inspection, size filtering, naming, HTTP retries, atomic dedup, and candidate fallback chains.

---

## 8. AI Agent Rules

- **Read `AGENTS.md` before modifying code.**
- **Do not silently enlarge scope.** If a requested feature violates v0.1.x scope, explain why or defer it.
- **Do not introduce large frameworks without justification.**
- **Do not rewrite working code for stylistic reasons only.**
- **Run tests after changes:** Always execute `go test ./...` and `go vet ./...`.
- **Update documentation:**
  - Update `README.md` when public CLI behavior or flags change.
  - Update `AGENTS.md` when project invariants or architecture change.

---

## 9. Definition of Done

Any task or PR is considered complete only when:
```bash
go test ./...
go vet ./...
go build ./...
```
All pass with zero errors and zero warnings.
