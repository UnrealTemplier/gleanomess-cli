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
	TotalSlots      int
	TotalCandidates int
	Downloaded      int
	Skipped         int
	Duplicates      int
	Errors          int
}

// Options configures the Downloader.
type Options struct {
	Workers       int
	LimitSize     int
	Verbose       bool
	OutputDir     string
	PageURL       string
	MaxImageBytes int64
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

// DownloadAll processes all image slots with a bounded worker pool.
func (d *Downloader) DownloadAll(ctx context.Context, slots []*scraper.Slot) (*Summary, error) {
	totalSlots := len(slots)
	totalCands := 0
	for _, s := range slots {
		totalCands += len(s.Candidates)
	}

	summary := &Summary{
		TotalSlots:      totalSlots,
		TotalCandidates: totalCands,
	}

	if totalSlots == 0 {
		return summary, nil
	}

	type job struct {
		index int
		slot  *scraper.Slot
	}

	jobs := make(chan job, totalSlots)
	for i, s := range slots {
		jobs <- job{index: i + 1, slot: s}
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

				res := d.processSlot(ctx, j.slot, j.index, totalSlots)

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

// processSlot tries ranked candidates in a slot in order until one succeeds or all fail.
func (d *Downloader) processSlot(ctx context.Context, slot *scraper.Slot, index, total int) *ItemResult {
	if len(slot.Candidates) == 0 {
		return &ItemResult{
			Index:  index,
			Total:  total,
			Status: StatusSkipped,
			Reason: "empty slot",
		}
	}

	var lastRes *ItemResult
	for candIdx, cand := range slot.Candidates {
		select {
		case <-ctx.Done():
			return &ItemResult{
				Index:     index,
				Total:     total,
				Candidate: cand,
				Status:    StatusError,
				Error:     ctx.Err(),
			}
		default:
		}

		res := d.processCandidate(ctx, cand, index, total)
		lastRes = res

		// If successfully downloaded, stop immediately and do not download fallback candidates.
		if res.Status == StatusDownloaded {
			return res
		}

		// If this exact URL or identical content was already downloaded, slot is satisfied.
		if res.Status == StatusDuplicate {
			return res
		}

		// Candidate failed (StatusError or StatusSkipped).
		// If more candidates exist in this slot, report fallback in verbose mode and try next candidate!
		if candIdx+1 < len(slot.Candidates) && d.options.Verbose {
			d.reportFallback(index, total, cand, res, slot.Candidates[candIdx+1])
		}
	}

	return lastRes
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

	// Fix #4: Read up to maxBytes + 1 to detect overflow without silent truncation
	maxBytes := d.options.MaxImageBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxImageBytes
	}
	limitedReader := io.LimitReader(reqResult.Response.Body, maxBytes+1)
	n, copyErr := io.Copy(writer, limitedReader)
	_ = tmpFile.Close()

	if copyErr != nil {
		res.Status = StatusError
		res.Error = fmt.Errorf("streaming response body: %w", copyErr)
		return res
	}

	// If n > maxBytes, response exceeded the safety limit! Fail and clean up temp file.
	if n > maxBytes {
		res.Status = StatusError
		res.Error = fmt.Errorf("image exceeds internal download size limit")
		return res
	}

	shaHex := hex.EncodeToString(hasher.Sum(nil))

	// Fast-path content deduplication pre-check
	if existingFile, isDup := d.contentTracker.Check(shaHex); isDup {
		res.Status = StatusDuplicate
		res.Reason = fmt.Sprintf("identical content to %s (sha256: %s)", existingFile, shaHex[:8])
		return res
	}

	// 4. Inspect image format and dimensions (without holding any lock)
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

	// 5. Size Filter Check: max(width, height) >= limit
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

	// 6. Fix #1: Atomic Finalize with ContentTracker
	contentDisposition := reqResult.Response.Header.Get("Content-Disposition")
	baseFilename := naming.ResolveFilename(contentDisposition, targetURL, info.Extension, shaHex)

	finalFilename, isDup, err := d.contentTracker.Finalize(shaHex, func() (string, error) {
		name := d.allocator.Allocate(baseFilename)
		finalPath := filepath.Join(d.options.OutputDir, name)
		if err := os.Rename(tmpPath, finalPath); err != nil {
			return "", fmt.Errorf("finalizing file: %w", err)
		}
		// Successfully renamed; clear tmpPath so deferred os.Remove won't delete final file
		tmpPath = ""
		return name, nil
	})

	if err != nil {
		res.Status = StatusError
		res.Error = err
		return res
	}

	if isDup {
		res.Status = StatusDuplicate
		res.Reason = fmt.Sprintf("identical content to %s (sha256: %s)", finalFilename, shaHex[:8])
		return res
	}

	res.Status = StatusDownloaded
	res.Filename = finalFilename
	return res
}

func (d *Downloader) reportFallback(index, total int, failed *scraper.Candidate, res *ItemResult, next *scraper.Candidate) {
	d.outMu.Lock()
	defer d.outMu.Unlock()

	var reason string
	if res.Error != nil {
		reason = res.Error.Error()
	} else if res.Reason != "" {
		reason = res.Reason
	} else {
		reason = string(res.Status)
	}

	fmt.Printf("[%d/%d] FALLBACK: %s failed (%s), trying %s [%s]\n",
		index, total, failed.ResolvedURL.String(), reason, next.ResolvedURL.String(), next.Source)
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
