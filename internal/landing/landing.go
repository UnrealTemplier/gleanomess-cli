package landing

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"

	"github.com/UnrealTemplier/gleanomess-cli/internal/downloader"
	"github.com/UnrealTemplier/gleanomess-cli/internal/scraper"
	"golang.org/x/net/html"
)

const maxLandingPageBytes = 20 * 1024 * 1024 // 20 MiB safety limit

type cachedPage struct {
	err        error
	isDirect   bool
	candidates []*scraper.Candidate
}

// Resolver resolves 1-hop landing pages for image slots that lack direct raster candidates.
type Resolver struct {
	client     *downloader.HTTPClient
	verbose    bool
	pageMu     sync.Mutex
	pageCache  map[string]*cachedPage
	tokenIndex map[string]*scraper.Candidate
}

// NewResolver creates a new Landing Resolver.
func NewResolver(client *downloader.HTTPClient, verbose bool) *Resolver {
	return &Resolver{
		client:     client,
		verbose:    verbose,
		pageCache:  make(map[string]*cachedPage),
		tokenIndex: make(map[string]*scraper.Candidate),
	}
}

// ResolveSlots resolves landing candidates for all eligible slots in slots.
func (r *Resolver) ResolveSlots(ctx context.Context, slots []*scraper.Slot, pageURL *url.URL) {
	if r == nil || len(slots) == 0 {
		return
	}

	for _, slot := range slots {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if slot.LandingURL == nil {
			continue
		}

		// If slot already has a high-priority direct raster candidate, no need for landing resolution
		if hasDirectHighPriorityCandidate(slot) {
			continue
		}

		// Same-origin restriction: ignore cross-origin landing URLs
		if !scraper.IsSameOrigin(pageURL, slot.LandingURL) {
			continue
		}

		r.resolveSlot(ctx, slot)
	}
}

func (r *Resolver) resolveSlot(ctx context.Context, slot *scraper.Slot) {
	sourceTokens := ExtractSlotTokens(slot)

	// 1. Check if an earlier landing page already discovered a candidate matching one of our tokens
	r.pageMu.Lock()
	var candidateFromIndex *scraper.Candidate
	for tok := range sourceTokens {
		if cand, exists := r.tokenIndex[tok]; exists {
			candidateFromIndex = cand
			break
		}
	}
	r.pageMu.Unlock()

	if candidateFromIndex != nil {
		r.promoteCandidate(slot, candidateFromIndex)
		if r.verbose {
			fmt.Printf("LANDING SUCCESS (cached index): %s -> %s\n", slot.LandingURL.String(), candidateFromIndex.ResolvedURL.String())
		}
		return
	}

	// 2. Fetch or retrieve landing page
	cached := r.fetchLandingPage(ctx, slot.LandingURL)
	if cached == nil || cached.err != nil {
		return
	}

	if cached.isDirect && len(cached.candidates) > 0 {
		r.promoteCandidate(slot, cached.candidates[0])
		if r.verbose {
			fmt.Printf("LANDING SUCCESS: %s (direct raster)\n", slot.LandingURL.String())
		}
		return
	}

	// 3. Correlate candidates from the landing page with this slot
	best := r.correlateCandidate(slot, cached.candidates)
	if best != nil {
		r.promoteCandidate(slot, best)
		if r.verbose {
			fmt.Printf("LANDING SUCCESS: %s -> %s\n", slot.LandingURL.String(), best.ResolvedURL.String())
		}
	} else if r.verbose {
		fmt.Printf("LANDING: %s (no correlated candidate found, falling back)\n", slot.LandingURL.String())
	}
}

func (r *Resolver) fetchLandingPage(ctx context.Context, landingURL *url.URL) *cachedPage {
	cleanURL := *landingURL
	cleanURL.Fragment = ""
	key := cleanURL.String()

	r.pageMu.Lock()
	if cached, found := r.pageCache[key]; found {
		r.pageMu.Unlock()
		return cached
	}
	r.pageMu.Unlock()

	if r.verbose {
		fmt.Printf("LANDING: %s\n", landingURL.String())
	}

	reqResult, err := r.client.GetWithRetry(ctx, landingURL.String(), false)
	if err != nil {
		cached := &cachedPage{err: err}
		r.pageMu.Lock()
		r.pageCache[key] = cached
		r.pageMu.Unlock()
		return cached
	}
	defer reqResult.Response.Body.Close()

	effectiveURL := landingURL
	if reqResult.FinalURL != "" {
		if u, err := url.Parse(reqResult.FinalURL); err == nil {
			effectiveURL = u
		}
	}

	// Check Content-Type: response may itself be a raster image
	contentType := reqResult.Response.Header.Get("Content-Type")
	if isRasterContentType(contentType) {
		cand := &scraper.Candidate{
			RawURL:      landingURL.String(),
			ResolvedURL: effectiveURL,
			Source:      scraper.SourceLandingImage,
			Priority:    110,
		}
		cached := &cachedPage{
			isDirect:   true,
			candidates: []*scraper.Candidate{cand},
		}
		r.pageMu.Lock()
		r.pageCache[key] = cached
		r.pageMu.Unlock()
		return cached
	}

	// Read limited HTML body
	limitedReader := io.LimitReader(reqResult.Response.Body, maxLandingPageBytes+1)
	var bodyBuf bytes.Buffer
	n, readErr := bodyBuf.ReadFrom(limitedReader)
	if readErr != nil && readErr != io.EOF {
		cached := &cachedPage{err: readErr}
		r.pageMu.Lock()
		r.pageCache[key] = cached
		r.pageMu.Unlock()
		return cached
	}

	if n > maxLandingPageBytes {
		cached := &cachedPage{err: fmt.Errorf("landing page exceeded maximum size (%d bytes)", maxLandingPageBytes)}
		r.pageMu.Lock()
		r.pageCache[key] = cached
		r.pageMu.Unlock()
		return cached
	}

	doc, parseErr := html.Parse(&bodyBuf)
	if parseErr != nil {
		cached := &cachedPage{err: parseErr}
		r.pageMu.Lock()
		r.pageCache[key] = cached
		r.pageMu.Unlock()
		return cached
	}

	// Scrape image slots from landing page
	landingSlots := scraper.ScrapeDoc(doc, effectiveURL)
	landingCandidates := scraper.FlattenCandidates(landingSlots)

	// Index high-priority candidates (e.g. SourceAnchorImage, SourceDataOriginal, SourcePictureSource, SourceImgSrcset)
	// for batch correlation across slots. Generic thumbnails (Priority <= 50) are not indexed to prevent cross-page pollution.
	r.pageMu.Lock()
	for _, cand := range landingCandidates {
		if cand.ResolvedURL != nil && cand.Priority >= 70 {
			for _, tok := range ExtractTokens(cand.ResolvedURL) {
				if _, exists := r.tokenIndex[tok]; !exists {
					r.tokenIndex[tok] = cand
				}
			}
		}
	}
	cached := &cachedPage{
		candidates: landingCandidates,
	}
	r.pageCache[key] = cached
	r.pageMu.Unlock()

	return cached
}

func (r *Resolver) correlateCandidate(slot *scraper.Slot, candidates []*scraper.Candidate) *scraper.Candidate {
	sourceTokens := ExtractSlotTokens(slot)
	if len(sourceTokens) == 0 || len(candidates) == 0 {
		return nil
	}

	type scoredCand struct {
		cand  *scraper.Candidate
		score int
	}

	var scored []scoredCand
	maxScore := 0

	for _, cand := range candidates {
		if cand.ResolvedURL == nil {
			continue
		}
		// Do not promote a URL that is already an existing candidate in this slot
		if isURLAlreadyInSlot(slot, cand.ResolvedURL) {
			continue
		}

		score := MatchScore(sourceTokens, cand.ResolvedURL)
		if score > 0 {
			scored = append(scored, scoredCand{cand: cand, score: score})
			if score > maxScore {
				maxScore = score
			}
		}
	}

	if maxScore == 0 || len(scored) == 0 {
		return nil
	}

	// Filter to candidates with maxScore
	var top []*scraper.Candidate
	for _, sc := range scored {
		if sc.score == maxScore {
			top = append(top, sc.cand)
		}
	}

	if len(top) == 1 {
		return top[0]
	}

	// Multiple candidates share maxScore: prefer candidate with highest priority
	highestPriority := -1
	for _, c := range top {
		if c.Priority > highestPriority {
			highestPriority = c.Priority
		}
	}

	var bestPriority []*scraper.Candidate
	for _, c := range top {
		if c.Priority == highestPriority {
			bestPriority = append(bestPriority, c)
		}
	}

	if len(bestPriority) == 1 {
		return bestPriority[0]
	}

	// Check if all remaining candidates point to the same URL
	firstURL := bestPriority[0].ResolvedURL.String()
	allSame := true
	for _, c := range bestPriority[1:] {
		if c.ResolvedURL.String() != firstURL {
			allSame = false
			break
		}
	}
	if allSame {
		return bestPriority[0]
	}

	// Ambiguous matches pointing to different URLs: reject rather than guessing!
	return nil
}

func isURLAlreadyInSlot(slot *scraper.Slot, targetURL *url.URL) bool {
	if slot == nil || targetURL == nil {
		return false
	}
	targetStr := targetURL.String()
	for _, c := range slot.Candidates {
		if c.ResolvedURL != nil && c.ResolvedURL.String() == targetStr {
			return true
		}
	}
	return false
}

func (r *Resolver) promoteCandidate(slot *scraper.Slot, cand *scraper.Candidate) {
	promoted := &scraper.Candidate{
		RawURL:      cand.RawURL,
		ResolvedURL: cand.ResolvedURL,
		Source:      scraper.SourceLandingImage,
		Priority:    110,
		Descriptor:  cand.Descriptor,
	}
	slot.Candidates = append([]*scraper.Candidate{promoted}, slot.Candidates...)
}

func hasDirectHighPriorityCandidate(slot *scraper.Slot) bool {
	for _, c := range slot.Candidates {
		if c.Source == scraper.SourceAnchorImage || c.Source == scraper.SourceDataOriginal {
			return true
		}
	}
	return false
}

func isRasterContentType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if idx := strings.IndexByte(ct, ';'); idx >= 0 {
		ct = strings.TrimSpace(ct[:idx])
	}
	switch ct {
	case "image/jpeg", "image/jpg", "image/png", "image/webp", "image/gif", "image/avif", "image/bmp", "image/tiff":
		return true
	default:
		return false
	}
}
