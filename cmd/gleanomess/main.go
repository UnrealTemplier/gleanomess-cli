package main

import (
	"context"
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
	"github.com/UnrealTemplier/gleanomess-cli/internal/scraper"
)

const version = "0.1.0"

var invalidHostCharsRegex = regexp.MustCompile(`[^a-zA-Z0-9.\-_]`)

func main() {
	exitCode := run(os.Args[1:])
	os.Exit(exitCode)
}

func run(args []string) int {
	fs := flag.NewFlagSet("gleanomess", flag.ContinueOnError)

	var (
		limitSize int
		outputDir string
		workers   int
		verbose   bool
	)

	fs.IntVar(&limitSize, "limit-size", 0, "filter images with max(width, height) >= N")
	fs.StringVar(&outputDir, "output-dir", "", "override base output directory")
	fs.IntVar(&workers, "workers", 4, "number of concurrent download workers")
	fs.BoolVar(&verbose, "verbose", false, "enable detailed verbose output")
	fs.BoolVar(&verbose, "v", false, "enable detailed verbose output (shorthand)")

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
	}
	fmt.Println()

	// Initialize HTTP Client
	client, err := downloader.NewHTTPClient(rawPageURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing HTTP client: %v\n", err)
		return 1
	}

	// Fetch HTML page
	pageResult, err := client.FetchPage(ctx, rawPageURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error fetching page %s: %v\n", rawPageURL, err)
		return 1
	}
	defer pageResult.Response.Body.Close()

	// Update base URL if redirected
	effectiveBaseURL := parsedURL
	if pageResult.FinalURL != "" {
		if u, err := url.Parse(pageResult.FinalURL); err == nil {
			effectiveBaseURL = u
		}
	}

	// Scrape HTML for image candidates
	candidates, err := scraper.Scrape(pageResult.Response.Body, effectiveBaseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing HTML from %s: %v\n", rawPageURL, err)
		return 1
	}

	fmt.Printf("Found %d unique image candidates.\n\n", len(candidates))
	if len(candidates) == 0 {
		fmt.Println("Done.")
		fmt.Println("Downloaded: 0")
		fmt.Println("Skipped:    0")
		fmt.Println("Duplicates: 0")
		fmt.Println("Errors:     0")
		return 0
	}

	// Run Downloader
	dl := downloader.NewDownloader(client, downloader.Options{
		Workers:   workers,
		LimitSize: limitSize,
		Verbose:   verbose,
		OutputDir: targetDir,
		PageURL:   rawPageURL,
	})

	summary, err := dl.DownloadAll(ctx, candidates)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error during download process: %v\n", err)
		return 1
	}

	fmt.Println()
	fmt.Println("Done.")
	fmt.Printf("Downloaded: %d\n", summary.Downloaded)
	fmt.Printf("Skipped:    %d\n", summary.Skipped)
	fmt.Printf("Duplicates: %d\n", summary.Duplicates)
	fmt.Printf("Errors:     %d\n", summary.Errors)

	return 0
}

// resolveTargetDir determines the output directory for a given base and parsed URL.
// Directory name is the hostname with only "www." prefix removed and characters sanitized.
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

	return filepath.Join(baseDir, sanitizedHost), nil
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
				case "limit-size", "output-dir", "workers":
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
