package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/UnrealTemplier/gleanomess-cli/internal/downloader"
	"github.com/UnrealTemplier/gleanomess-cli/internal/landing"
	"github.com/UnrealTemplier/gleanomess-cli/internal/scraper"
	"golang.org/x/net/html"
)

const version = "0.2.0"

var (
	invalidHostCharsRegex = regexp.MustCompile(`[^a-zA-Z0-9.\-_]`)
	nonAlphaNumRegex      = regexp.MustCompile(`[^a-zA-Z0-9_\-]+`)
	multiDashRegex        = regexp.MustCompile(`-+`)
)

func main() {
	exitCode := run(os.Args[1:])
	os.Exit(exitCode)
}

func run(args []string) int {
	fs := flag.NewFlagSet("gleanomess", flag.ContinueOnError)

	var (
		limitSize       int
		outputDir       string
		workers         int
		verbose         bool
		showVer         bool
		resolveLandings bool
		allPages        bool
		maxPages        int
	)

	fs.IntVar(&limitSize, "limit-size", 0, "filter images with max(width, height) >= N")
	fs.StringVar(&outputDir, "output-dir", "", "override base output directory")
	fs.IntVar(&workers, "workers", 4, "number of concurrent download workers")
	fs.BoolVar(&verbose, "verbose", false, "enable detailed verbose output")
	fs.BoolVar(&verbose, "v", false, "enable detailed verbose output (shorthand)")
	fs.BoolVar(&showVer, "version", false, "show program version and exit")
	fs.BoolVar(&resolveLandings, "resolve-landings", false, "resolve 1-hop landing pages for image slots missing direct raster candidates")
	fs.BoolVar(&allPages, "all-pages", false, "enable sequential multi-page traversal following pagination")
	fs.IntVar(&maxPages, "max-pages", 0, "maximum number of HTML pages to process (0 = unlimited with --all-pages)")

	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "GleanoMess %s - Extract and download raster images from a webpage\n\n", version)
		fmt.Fprintf(fs.Output(), "Usage:\n")
		fmt.Fprintf(fs.Output(), "  gleanomess <URL> [flags]\n")
		fmt.Fprintf(fs.Output(), "  gleanomess [flags] <URL>\n\n")
		fmt.Fprintf(fs.Output(), "Flags:\n")
		fs.PrintDefaults()
	}

	normalizedArgs := normalizeArgs(args)
	if err := fs.Parse(normalizedArgs); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	if showVer {
		fmt.Printf("GleanoMess %s\n", version)
		return 0
	}

	remaining := fs.Args()
	if len(remaining) == 0 {
		fmt.Fprintln(os.Stderr, "Error: missing required webpage URL.")
		fs.Usage()
		return 2
	}
	if len(remaining) > 1 {
		fmt.Fprintf(os.Stderr, "Error: expected exactly 1 URL, got %d arguments: %v\n", len(remaining), remaining)
		fs.Usage()
		return 2
	}

	rawPageURL := remaining[0]
	parsedURL, err := url.Parse(rawPageURL)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		fmt.Fprintf(os.Stderr, "Error: invalid URL %q (scheme and host required)\n", rawPageURL)
		return 2
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		fmt.Fprintf(os.Stderr, "Error: unsupported URL scheme %q (must be http or https)\n", parsedURL.Scheme)
		return 2
	}

	if limitSize < 0 {
		fmt.Fprintf(os.Stderr, "Error: --limit-size must be non-negative, got %d\n", limitSize)
		return 2
	}
	if workers <= 0 {
		fmt.Fprintf(os.Stderr, "Error: --workers must be greater than 0, got %d\n", workers)
		return 2
	}
	if maxPages < 0 {
		fmt.Fprintf(os.Stderr, "Error: --max-pages must be non-negative, got %d\n", maxPages)
		return 2
	}

	targetDir, err := resolveTargetDir(outputDir, parsedURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error determining output directory: %v\n", err)
		return 1
	}

	if err := os.MkdirAll(targetDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating output directory %s: %v\n", targetDir, err)
		return 1
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	fmt.Printf("GleanoMess %s\n", version)
	fmt.Printf("Page: %s\n", rawPageURL)
	if verbose {
		fmt.Printf("Output: %s\n", targetDir)
		fmt.Printf("Workers: %d\n", workers)
		if limitSize > 0 {
			fmt.Printf("Limit Size: max(width, height) >= %d\n", limitSize)
		}
		if resolveLandings {
			fmt.Println("Resolve Landings: enabled (1-hop same-origin)")
		}
		if allPages || maxPages > 1 {
			if maxPages > 0 {
				fmt.Printf("Pagination: enabled (max %d pages)\n", maxPages)
			} else {
				fmt.Println("Pagination: enabled (all pages)")
			}
		}
	}
	fmt.Println()

	// Initialize HTTP Client
	client, err := downloader.NewHTTPClient(rawPageURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing HTTP client: %v\n", err)
		return 1
	}

	// Downloader manages bounded concurrency image downloading and deduplication across pages
	dl := downloader.NewDownloader(client, downloader.Options{
		Workers:   workers,
		LimitSize: limitSize,
		Verbose:   verbose,
		OutputDir: targetDir,
		PageURL:   rawPageURL,
	})

	landingResolver := landing.NewResolver(client, verbose)

	paginationEnabled := allPages || maxPages > 1
	visitedPages := make(map[string]bool)
	currentPageURL := rawPageURL
	pageCount := 0

	var totalSummary downloader.Summary

	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(os.Stderr, "\nOperation canceled.")
			return 1
		default:
		}

		parsedCurr, err := url.Parse(currentPageURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing page URL %s: %v\n", currentPageURL, err)
			totalSummary.Errors++
			break
		}

		cleanCurr := *parsedCurr
		cleanCurr.Fragment = ""
		cleanCurr.Scheme = strings.ToLower(cleanCurr.Scheme)
		cleanCurr.Host = strings.ToLower(cleanCurr.Host)
		canonicalURLStr := cleanCurr.String()

		if visitedPages[canonicalURLStr] {
			if verbose {
				fmt.Printf("PAGE already visited: %s (loop prevented)\n", currentPageURL)
			}
			break
		}
		visitedPages[canonicalURLStr] = true
		pageCount++

		if verbose {
			fmt.Printf("PAGE %d: %s\n", pageCount, currentPageURL)
		}

		// Fetch HTML page
		pageResult, err := client.FetchPage(ctx, currentPageURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching page %s: %v\n", currentPageURL, err)
			totalSummary.Errors++
			break
		}

		doc, err := html.Parse(pageResult.Response.Body)
		pageResult.Response.Body.Close()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing HTML from %s: %v\n", currentPageURL, err)
			totalSummary.Errors++
			break
		}

		effectiveBaseURL := parsedCurr
		if pageResult.FinalURL != "" {
			if u, err := url.Parse(pageResult.FinalURL); err == nil {
				effectiveBaseURL = u
			}
		}

		// Scrape HTML for image candidates and slots
		slots := scraper.ScrapeDoc(doc, effectiveBaseURL)

		if verbose {
			totalCandidates := 0
			for _, s := range slots {
				totalCandidates += len(s.Candidates)
			}
			fmt.Printf("Found %d image slots (%d candidates) on page %d.\n", len(slots), totalCandidates, pageCount)
		} else if pageCount == 1 && !paginationEnabled {
			fmt.Printf("Found %d image slots.\n\n", len(slots))
		}

		// One-hop landing page resolution
		if resolveLandings && len(slots) > 0 {
			landingResolver.ResolveSlots(ctx, slots, effectiveBaseURL)
		}

		// Download images for this page
		if len(slots) > 0 {
			pageSummary, err := dl.DownloadAll(ctx, slots)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error during download process: %v\n", err)
				return 1
			}
			totalSummary.Downloaded += pageSummary.Downloaded
			totalSummary.Skipped += pageSummary.Skipped
			totalSummary.Duplicates += pageSummary.Duplicates
			totalSummary.Errors += pageSummary.Errors
			totalSummary.TotalSlots += pageSummary.TotalSlots
			totalSummary.TotalCandidates += pageSummary.TotalCandidates
		}

		if !paginationEnabled {
			break
		}
		if maxPages > 0 && pageCount >= maxPages {
			if verbose {
				fmt.Printf("Reached maximum page limit (%d), stopping traversal.\n", maxPages)
			}
			break
		}

		nextURL := scraper.FindNextPage(doc, effectiveBaseURL)
		if nextURL == nil {
			if verbose {
				fmt.Println("No next page link found, pagination traversal complete.")
			}
			break
		}

		currentPageURL = nextURL.String()
		if verbose {
			fmt.Println()
		}
	}

	fmt.Println()
	fmt.Println("Done.")
	if paginationEnabled || pageCount > 1 {
		fmt.Printf("Pages processed:   %d\n", pageCount)
		fmt.Printf("Images downloaded: %d\n", totalSummary.Downloaded)
	} else {
		fmt.Printf("Downloaded:        %d\n", totalSummary.Downloaded)
	}
	fmt.Printf("Skipped:           %d\n", totalSummary.Skipped)
	fmt.Printf("Duplicates:        %d\n", totalSummary.Duplicates)
	fmt.Printf("Errors:            %d\n", totalSummary.Errors)

	return 0
}

// resolveTargetDir determines the output directory for a given base and parsed URL.
// Directory structure is: <baseDir>/<sanitizedHost>/<pageFolder>
// where pageFolder is derived from the URL path slug and a deterministic short hash.
func resolveTargetDir(customBaseDir string, u *url.URL) (string, error) {
	host := u.Hostname()
	if host == "" {
		host = u.Host
	}
	if host == "" {
		return "", fmt.Errorf("URL has no hostname")
	}

	// Remove only prefix "www." (case-insensitive)
	lowerHost := strings.ToLower(host)
	if strings.HasPrefix(lowerHost, "www.") {
		host = host[4:]
	}

	// Sanitize hostname: keep letters, numbers, dot, dash, underscore
	sanitizedHost := invalidHostCharsRegex.ReplaceAllString(host, "_")
	sanitizedHost = strings.Trim(sanitizedHost, " ._")
	if sanitizedHost == "" {
		sanitizedHost = "site"
	}

	pageFolder := buildPageFolderName(u)

	var baseDir string
	if customBaseDir != "" {
		baseDir = customBaseDir
	} else {
		exePath, err := os.Executable()
		if err != nil {
			return "", fmt.Errorf("retrieving executable path: %w", err)
		}
		realPath, err := filepath.EvalSymlinks(exePath)
		if err == nil {
			exePath = realPath
		}
		baseDir = filepath.Dir(exePath)
	}

	return filepath.Join(baseDir, sanitizedHost, pageFolder), nil
}

// buildPageFolderName creates a deterministic, filesystem-safe folder name
// composed of a normalized path slug and a short SHA-256 hash of the initial URL.
func buildPageFolderName(u *url.URL) string {
	cleanURL := *u
	cleanURL.Scheme = strings.ToLower(cleanURL.Scheme)
	cleanURL.Host = strings.ToLower(cleanURL.Host)
	cleanURL.Fragment = ""

	hasher := sha256.New()
	hasher.Write([]byte(cleanURL.String()))
	shortHash := hex.EncodeToString(hasher.Sum(nil))[:8]

	pathStr := u.Path
	if unescaped, err := url.PathUnescape(pathStr); err == nil {
		pathStr = unescaped
	}

	slug := strings.Trim(pathStr, "/")
	slug = nonAlphaNumRegex.ReplaceAllString(slug, "-")
	slug = multiDashRegex.ReplaceAllString(slug, "-")
	slug = strings.ToLower(slug)
	slug = strings.Trim(slug, "-_.")

	const maxSlugLen = 64
	if len(slug) > maxSlugLen {
		slug = strings.TrimRight(slug[:maxSlugLen], "-_.")
	}

	if slug == "" {
		slug = "root"
	}

	return slug + "-" + shortHash
}

func normalizeArgs(args []string) []string {
	var flags []string
	var pos []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if strings.HasPrefix(arg, "-") {
			flags = append(flags, arg)
			flagName := strings.TrimLeft(arg, "-")
			if !strings.Contains(flagName, "=") {
				switch flagName {
				case "limit-size", "output-dir", "workers", "max-pages":
					if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
						i++
						flags = append(flags, args[i])
					}
				}
			}
		} else {
			pos = append(pos, arg)
		}
	}
	return append(flags, pos...)
}
