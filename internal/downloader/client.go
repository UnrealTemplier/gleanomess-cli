package downloader

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36 GleanoMess/0.1"
	maxRedirects     = 10
	maxImageBytes    = 500 * 1024 * 1024 // 500 MB safety limit
)

// HTTPClient wraps an http.Client with cookie support, redirect limits, and retry logic.
type HTTPClient struct {
	client    *http.Client
	pageURL   string
	userAgent string
}

// NewHTTPClient creates a configured HTTP client.
func NewHTTPClient(pageURL string) (*HTTPClient, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("creating cookie jar: %w", err)
	}

	client := &http.Client{
		Jar:     jar,
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("stopped after %d redirects", maxRedirects)
			}
			return nil
		},
	}

	return &HTTPClient{
		client:    client,
		pageURL:   pageURL,
		userAgent: defaultUserAgent,
	}, nil
}

// RequestResult contains the response and metadata from an HTTP request.
type RequestResult struct {
	Response   *http.Response
	Redirects  []string
	Attempts   int
	FinalURL   string
	StatusCode int
}

// GetWithRetry performs an HTTP GET with up to 3 attempts for temporary errors.
func (h *HTTPClient) GetWithRetry(ctx context.Context, targetURL string, isImage bool) (*RequestResult, error) {
	const maxAttempts = 3

	var (
		lastErr      error
		lastResp     *http.Response
		redirectURLs []string
	)

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
		if err != nil {
			return nil, fmt.Errorf("creating HTTP request: %w", err)
		}

		req.Header.Set("User-Agent", h.userAgent)
		if h.pageURL != "" {
			req.Header.Set("Referer", h.pageURL)
		}

		if isImage {
			req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8")
		} else {
			req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		}

		// Track redirects per request
		redirectURLs = nil
		client := *h.client
		client.CheckRedirect = func(r *http.Request, via []*http.Request) error {
			redirectURLs = append(redirectURLs, r.URL.String())
			if len(via) >= maxRedirects {
				return fmt.Errorf("stopped after %d redirects", maxRedirects)
			}
			return nil
		}

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			if shouldRetryError(err) && attempt < maxAttempts {
				h.sleepBackoff(ctx, attempt, nil)
				continue
			}
			return nil, fmt.Errorf("HTTP request failed: %w", err)
		}

		// Check HTTP status code
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			finalURL := targetURL
			if resp.Request != nil && resp.Request.URL != nil {
				finalURL = resp.Request.URL.String()
			}
			return &RequestResult{
				Response:   resp,
				Redirects:  redirectURLs,
				Attempts:   attempt,
				FinalURL:   finalURL,
				StatusCode: resp.StatusCode,
			}, nil
		}

		lastResp = resp
		statusCode := resp.StatusCode

		if shouldRetryStatus(statusCode) && attempt < maxAttempts {
			retryAfterHeader := resp.Header.Get("Retry-After")
			resp.Body.Close()
			h.sleepBackoff(ctx, attempt, parseRetryAfter(retryAfterHeader))
			continue
		}

		// Permanent error or attempts exhausted
		return &RequestResult{
			Response:   resp,
			Redirects:  redirectURLs,
			Attempts:   attempt,
			FinalURL:   targetURL,
			StatusCode: statusCode,
		}, fmt.Errorf("HTTP %d: %s", statusCode, http.StatusText(statusCode))
	}

	if lastResp != nil {
		lastResp.Body.Close()
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("request failed after %d attempts", maxAttempts)
}

func shouldRetryStatus(statusCode int) bool {
	switch statusCode {
	case 408, 429, 500, 502, 503, 504:
		return true
	default:
		return false
	}
}

func shouldRetryError(err error) bool {
	if err == nil {
		return false
	}
	// Check context cancellation
	if strings.Contains(err.Error(), "context canceled") {
		return false
	}
	return true
}

func (h *HTTPClient) sleepBackoff(ctx context.Context, attempt int, retryAfter *time.Duration) {
	var wait time.Duration
	if retryAfter != nil && *retryAfter > 0 {
		wait = *retryAfter
		// Cap retry-after to 10 seconds for CLI
		if wait > 10*time.Second {
			wait = 10 * time.Second
		}
	} else {
		// Exponential backoff: 200ms, 400ms, 800ms + jitter
		base := time.Duration(1<<attempt) * 100 * time.Millisecond
		jitter := time.Duration(rand.Intn(100)) * time.Millisecond
		wait = base + jitter
	}

	select {
	case <-time.After(wait):
	case <-ctx.Done():
	}
}

func parseRetryAfter(header string) *time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil
	}
	// Try integer seconds
	if secs, err := strconv.Atoi(header); err == nil && secs >= 0 {
		d := time.Duration(secs) * time.Second
		return &d
	}
	// Try HTTP-date
	if t, err := http.ParseTime(header); err == nil {
		d := time.Until(t)
		if d > 0 {
			return &d
		}
	}
	return nil
}

// FetchPage retrieves the HTML content of the page.
func (h *HTTPClient) FetchPage(ctx context.Context, rawURL string) (*RequestResult, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("unsupported URL scheme: %s (must be http or https)", u.Scheme)
	}

	res, err := h.GetWithRetry(ctx, rawURL, false)
	if err != nil {
		return nil, err
	}
	return res, nil
}
