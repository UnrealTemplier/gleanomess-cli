package downloader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sync"

	"github.com/UnrealTemplier/gleanomess-cli/internal/dedup"
	"github.com/UnrealTemplier/gleanomess-cli/internal/imageinfo"
	"github.com/UnrealTemplier/gleanomess-cli/internal/naming"
	"github.com/UnrealTemplier/gleanomess-cli/internal/scraper"
)

// ItemStatus represents the final status of a candidate processing attempt.
type ItemStatus string

const (
	StatusDownloaded ItemStatus = "Downloaded"
	StatusSkipped    ItemStatus = "Skipped"
	StatusDuplicate  ItemStatus = "Duplicate"
	StatusError      ItemStatus = "Error"
)

// ItemResult holds the processing result of an individual image candidate.
type ItemResult struct {
	Index      int
	Total      int
	Candidate  *scraper.Candidate
	Status     ItemStatus
	Filename   string
	Format     string
	Width      int
	Height     int
	Reason     string
	Attempts   int
	StatusCode int
	Error      error
}

// Summary stores the aggregated counts of the download job.
type Summary struct {
	TotalCandidates int
	Downloaded      int
	Skipped         int
	Duplicates      int
	Errors          int
}

// Options configures the Downloader.
type Options struct {
	Workers   int
	LimitSize int
	Verbose   bool
	OutputDir string
	PageURL   string
}

// Downloader manages concurrent downloading, validating, and saving of images.
type Downloader struct {
	client         *HTTPClient
	options        Options
	allocator      *naming.Allocator
	contentTracker *dedup.ContentTracker
	urlTracker     *dedup.URLTracker
	outMu          sync.Mutex
}

// NewDownloader creates a Downloader with the specified options and HTTP client.
func NewDownloader(client *HTTPClient, opts Options) *Downloader {
	if opts.Workers <= 0 {
		opts.Workers = 4
	}
	return &Downloader{
		client:         client,
		options:        opts,
		allocator:      naming.NewAllocator(opts.OutputDir),
		contentTracker: dedup.NewContentTracker(),
		urlTracker:     dedup.NewURLTracker(),
	}
}

// DownloadAll processes all candidates with a bounded worker pool.
func (d *Downloader) DownloadAll(ctx context.Context, candidates []*scraper.Candidate) (*Summary, error) {
	total := len(candidates)
	summary := &Summary{
		TotalCandidates: total,
	}

	if total == 0 {
		return summary, nil
	}

	type job struct {
		index int
		cand  *scraper.Candidate
	}

	jobs := make(chan job, total)
	for i, c := range candidates {
		jobs <- job{index: i + 1, cand: c}
	}
	close(jobs)

	var wg sync.WaitGroup
	var sumMu sync.Mutex

	for w := 0; w < d.options.Workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				select {
				case <-ctx.Done():
					return
				default:
				}

				res := d.processCandidate(ctx, j.cand, j.index, total)

				sumMu.Lock()
				switch res.Status {
				case StatusDownloaded:
					summary.Downloaded++
				case StatusSkipped:
					summary.Skipped++
				case StatusDuplicate:
					summary.Duplicates++
				case StatusError:
					summary.Errors++
				}
				sumMu.Unlock()

				d.reportResult(res)
			}
		}()
	}

	wg.Wait()
	return summary, nil
}

func (d *Downloader) processCandidate(ctx context.Context, cand *scraper.Candidate, index, total int) *ItemResult {
	res := &ItemResult{
		Index:     index,
		Total:     total,
		Candidate: cand,
	}

	targetURL := cand.ResolvedURL.String()

	// 1. URL Deduplication
	if !d.urlTracker.Add(targetURL) {
		res.Status = StatusDuplicate
		res.Reason = "URL already processed"
		return res
	}

	// 2. Fetch image via HTTP with retry
	reqResult, err := d.client.GetWithRetry(ctx, targetURL, true)
	if err != nil {
		res.Status = StatusError
		res.Error = err
		if reqResult != nil {
			res.StatusCode = reqResult.StatusCode
			res.Attempts = reqResult.Attempts
		}
		return res
	}
	defer reqResult.Response.Body.Close()

	res.StatusCode = reqResult.StatusCode
	res.Attempts = reqResult.Attempts

	// 3. Stream body to temporary file in output directory while computing SHA-256
	tmpFile, err := os.CreateTemp(d.options.OutputDir, ".gleanomess-*.tmp")
	if err != nil {
		res.Status = StatusError
		res.Error = fmt.Errorf("creating temp file: %w", err)
		return res
	}
	tmpPath := tmpFile.Name()
	defer func() {
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()

	hasher := sha256.New()
	writer := io.MultiWriter(tmpFile, hasher)

	// Enforce safety response limit
	limitedReader := io.LimitReader(reqResult.Response.Body, maxImageBytes)
	_, copyErr := io.Copy(writer, limitedReader)
	_ = tmpFile.Close()

	if copyErr != nil {
		res.Status = StatusError
		res.Error = fmt.Errorf("streaming response body: %w", copyErr)
		return res
	}

	shaHex := hex.EncodeToString(hasher.Sum(nil))

	// 4. Content Deduplication Check
	if existingFile, isDup := d.contentTracker.Check(shaHex); isDup {
		res.Status = StatusDuplicate
		res.Reason = fmt.Sprintf("identical content to %s (sha256: %s)", existingFile, shaHex[:8])
		return res
	}

	// 5. Inspect image format and dimensions
	f, err := os.Open(tmpPath)
	if err != nil {
		res.Status = StatusError
		res.Error = fmt.Errorf("opening downloaded temp file: %w", err)
		return res
	}
	contentType := reqResult.Response.Header.Get("Content-Type")
	urlExt := path.Ext(cand.ResolvedURL.Path)

	info, err := imageinfo.Inspect(f, contentType, urlExt)
	_ = f.Close()

	if err != nil {
		res.Status = StatusSkipped
		res.Reason = fmt.Sprintf("unsupported format or invalid image: %v", err)
		return res
	}

	res.Format = info.Format
	res.Width = info.Width
	res.Height = info.Height

	// 6. Size Filter Check
	if d.options.LimitSize > 0 {
		maxDim := info.Width
		if info.Height > maxDim {
			maxDim = info.Height
		}
		if maxDim < d.options.LimitSize {
			res.Status = StatusSkipped
			res.Reason = fmt.Sprintf("dimensions %dx%d (max %d < %d)", info.Width, info.Height, maxDim, d.options.LimitSize)
			return res
		}
	}

	// 7. Resolve Final Filename and Allocate Unique Name
	contentDisposition := reqResult.Response.Header.Get("Content-Disposition")
	baseFilename := naming.ResolveFilename(contentDisposition, targetURL, info.Extension, shaHex)
	finalFilename := d.allocator.Allocate(baseFilename)
	finalPath := filepath.Join(d.options.OutputDir, finalFilename)

	// 8. Atomic Rename to Final Destination
	if err := os.Rename(tmpPath, finalPath); err != nil {
		res.Status = StatusError
		res.Error = fmt.Errorf("finalizing file: %w", err)
		return res
	}

	// Mark tempPath as empty so deferred os.Remove is a no-op
	tmpPath = ""

	// 9. Register in Content Deduplication
	d.contentTracker.Register(shaHex, finalFilename)

	res.Status = StatusDownloaded
	res.Filename = finalFilename
	return res
}

func (d *Downloader) reportResult(res *ItemResult) {
	d.outMu.Lock()
	defer d.outMu.Unlock()

	if d.options.Verbose {
		switch res.Status {
		case StatusDownloaded:
			fmt.Printf("[%d/%d] %s (%dx%d, %s, HTTP %d) [source: %s, url: %s]\n",
				res.Index, res.Total, res.Filename, res.Width, res.Height, res.Format, res.StatusCode, res.Candidate.Source, res.Candidate.ResolvedURL.String())
		case StatusSkipped:
			fmt.Printf("[%d/%d] SKIPPED: %s [source: %s, url: %s]\n",
				res.Index, res.Total, res.Reason, res.Candidate.Source, res.Candidate.ResolvedURL.String())
		case StatusDuplicate:
			fmt.Printf("[%d/%d] DUPLICATE: %s [source: %s, url: %s]\n",
				res.Index, res.Total, res.Reason, res.Candidate.Source, res.Candidate.ResolvedURL.String())
		case StatusError:
			fmt.Printf("[%d/%d] ERROR: %v [source: %s, url: %s]\n",
				res.Index, res.Total, res.Error, res.Candidate.Source, res.Candidate.ResolvedURL.String())
		}
	} else {
		switch res.Status {
		case StatusDownloaded:
			fmt.Printf("[%d/%d] %s\n", res.Index, res.Total, res.Filename)
		case StatusError:
			// In compact mode, show non-fatal error line
			fmt.Printf("[%d/%d] error: %v\n", res.Index, res.Total, res.Error)
		default:
			// Skipped and duplicates are accounted for in the summary
		}
	}
}
