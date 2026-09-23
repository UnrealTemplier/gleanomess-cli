package scraper

import (
	"encoding/json"
	"io"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/UnrealTemplier/gleanomess-cli/internal/imageinfo"
	"golang.org/x/net/html"
)

// CandidateSource describes where an image URL was found.
type CandidateSource string

const (
	SourceAnchorImage   CandidateSource = "a[href]"
	SourceDataOriginal  CandidateSource = "data-original"
	SourcePictureSource CandidateSource = "picture/source"
	SourceImgSrcset     CandidateSource = "img[srcset]"
	SourceDataLazy      CandidateSource = "data-lazy"
	SourceDataSrc       CandidateSource = "data-src"
	SourceImgSrc        CandidateSource = "img[src]"
	SourceStandaloneA   CandidateSource = "a[href-standalone]"
	SourceMetaOG        CandidateSource = "meta[og:image]"
	SourceMetaTwitter   CandidateSource = "meta[twitter:image]"
	SourceJSONLD        CandidateSource = "json-ld"
	SourceCSSInline     CandidateSource = "css[style]"
	SourceCSSBlock      CandidateSource = "css[style-tag]"
	SourceLandingImage  CandidateSource = "landing"
)

// Candidate represents a discovered image asset candidate.
type Candidate struct {
	RawURL      string
	ResolvedURL *url.URL
	Source      CandidateSource
	Priority    int
	Descriptor  string // e.g. "1600w" or "2x"
}

// Slot represents a logical image slot on a webpage, containing one or more ranked candidates.
// Candidates are ordered by priority (highest priority first) to form a fallback chain.
type Slot struct {
	Candidates []*Candidate
	LandingURL *url.URL
}

// Best returns the highest-priority candidate in the slot, or nil if empty.
func (s *Slot) Best() *Candidate {
	if len(s.Candidates) == 0 {
		return nil
	}
	return s.Candidates[0]
}

// FlattenCandidates flattens all candidates across slots into a slice.
func FlattenCandidates(slots []*Slot) []*Candidate {
	var result []*Candidate
	for _, s := range slots {
		result = append(result, s.Candidates...)
	}
	return result
}

var cssURLRegex = regexp.MustCompile(`(?i)url\(\s*(?:['"]?)([^'")]+)(?:['"]?)\s*\)`)

// ScrapeDoc extracts ranked image slots from an already parsed HTML document node.
func ScrapeDoc(doc *html.Node, pageURL *url.URL) []*Slot {
	baseURL := *pageURL
	findBaseHref(doc, &baseURL)

	var slots []*Slot
	addSlot := func(s *Slot) {
		if s == nil || len(s.Candidates) == 0 {
			return
		}
		slots = append(slots, s)
	}

	traverseDOM(doc, &baseURL, addSlot)
	return slots
}

// Scrape extracts ranked image slots from an HTML document.
func Scrape(r io.Reader, pageURL *url.URL) ([]*Slot, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, err
	}
	return ScrapeDoc(doc, pageURL), nil
}

func findBaseHref(n *html.Node, baseURL *url.URL) {
	if n.Type == html.ElementNode && strings.EqualFold(n.Data, "base") {
		for _, attr := range n.Attr {
			if strings.EqualFold(attr.Key, "href") {
				raw := strings.TrimSpace(attr.Val)
				if raw != "" {
					if ref, err := url.Parse(raw); err == nil {
						*baseURL = *baseURL.ResolveReference(ref)
					}
				}
				return
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		findBaseHref(c, baseURL)
	}
}

type anchorContext struct {
	rawHref  string
	consumed bool
}

type pictureContext struct {
	sources []sourceEntry
}

type sourceEntry struct {
	srcset     string
	dataSource string
}

func traverseDOM(n *html.Node, baseURL *url.URL, addSlot func(*Slot)) {
	walk(n, baseURL, nil, nil, addSlot)
}

func walk(
	n *html.Node,
	baseURL *url.URL,
	currAnchor *anchorContext,
	currPicture *pictureContext,
	addSlot func(*Slot),
) {
	if n == nil {
		return
	}

	var nextAnchor = currAnchor
	var nextPicture = currPicture

	if n.Type == html.ElementNode {
		tag := strings.ToLower(n.Data)

		// Check inline style attribute on any element
		for _, attr := range n.Attr {
			if strings.EqualFold(attr.Key, "style") {
				extractCSSURLs(attr.Val, baseURL, SourceCSSInline, 20, addSlot)
			}
		}

		switch tag {
		case "a":
			href := getAttr(n, "href")
			if href != "" {
				nextAnchor = &anchorContext{
					rawHref:  href,
					consumed: false,
				}
			}

		case "picture":
			nextPicture = &pictureContext{}

		case "source":
			if currPicture != nil {
				srcset := getAttr(n, "srcset")
				dataSrcset := getAttr(n, "data-srcset")
				if srcset != "" || dataSrcset != "" {
					currPicture.sources = append(currPicture.sources, sourceEntry{
						srcset:     srcset,
						dataSource: dataSrcset,
					})
				}
			}

		case "img":
			// Process image slot
			slot := processImageSlot(n, baseURL, currAnchor, currPicture)
			if slot != nil {
				addSlot(slot)
				if currAnchor != nil {
					for _, c := range slot.Candidates {
						if c.Source == SourceAnchorImage {
							currAnchor.consumed = true
							break
						}
					}
				}
			}

		case "meta":
			processMetaTag(n, baseURL, addSlot)

		case "script":
			typeVal := getAttr(n, "type")
			if strings.EqualFold(typeVal, "application/ld+json") {
				processJSONLD(n, baseURL, addSlot)
			}

		case "style":
			if n.FirstChild != nil && n.FirstChild.Type == html.TextNode {
				extractCSSURLs(n.FirstChild.Data, baseURL, SourceCSSBlock, 20, addSlot)
			}
		}
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c, baseURL, nextAnchor, nextPicture, addSlot)
	}

	// On exiting <a>, if it pointed to a direct raster image and was not consumed by an inner <img>,
	// emit it as a standalone anchor slot.
	if n.Type == html.ElementNode && strings.ToLower(n.Data) == "a" && nextAnchor != nil && !nextAnchor.consumed {
		if isDirectRasterURL(nextAnchor.rawHref) {
			if resolved := resolveURL(baseURL, nextAnchor.rawHref); resolved != nil {
				addSlot(&Slot{
					Candidates: []*Candidate{
						{
							RawURL:      nextAnchor.rawHref,
							ResolvedURL: resolved,
							Source:      SourceStandaloneA,
							Priority:    40,
						},
					},
				})
			}
		}
	}
}

func processImageSlot(
	imgNode *html.Node,
	baseURL *url.URL,
	currAnchor *anchorContext,
	currPicture *pictureContext,
) *Slot {
	var candidates []*Candidate
	var landingURL *url.URL

	// 1. Wrapping <a href> pointing to a raster image (highest priority) or landing page
	if currAnchor != nil && currAnchor.rawHref != "" {
		if resolved := resolveURL(baseURL, currAnchor.rawHref); resolved != nil {
			if isDirectRasterURL(currAnchor.rawHref) {
				candidates = append(candidates, &Candidate{
					RawURL:      currAnchor.rawHref,
					ResolvedURL: resolved,
					Source:      SourceAnchorImage,
					Priority:    100,
				})
				currAnchor.consumed = true
			} else if IsSameOrigin(baseURL, resolved) {
				landingURL = resolved
				currAnchor.consumed = true
			}
		}
	}

	// 2. data-original / data-full / data-large attributes
	origAttrs := []string{
		"data-original", "data-original-src",
		"data-full", "data-full-src", "data-fullsize",
		"data-large", "data-large-src",
	}
	for _, attrName := range origAttrs {
		val := getAttr(imgNode, attrName)
		if val != "" {
			if resolved := resolveURL(baseURL, val); resolved != nil {
				candidates = append(candidates, &Candidate{
					RawURL:      val,
					ResolvedURL: resolved,
					Source:      SourceDataOriginal,
					Priority:    90,
				})
				break
			}
		}
	}

	// 3. <picture><source> elements
	if currPicture != nil && len(currPicture.sources) > 0 {
		var allSourceCands []SrcsetCandidate
		for _, s := range currPicture.sources {
			if s.srcset != "" {
				allSourceCands = append(allSourceCands, ParseSrcset(s.srcset)...)
			}
			if s.dataSource != "" {
				allSourceCands = append(allSourceCands, ParseSrcset(s.dataSource)...)
			}
		}
		SortSrcsetCandidatesDesc(allSourceCands)
		for idx, sc := range allSourceCands {
			if resolved := resolveURL(baseURL, sc.URL); resolved != nil {
				p := 80
				if idx > 0 {
					p = 78
				}
				candidates = append(candidates, &Candidate{
					RawURL:      sc.URL,
					ResolvedURL: resolved,
					Source:      SourcePictureSource,
					Priority:    p,
					Descriptor:  sc.Descriptor,
				})
				if idx >= 2 {
					break
				}
			}
		}
	}

	// 4. img srcset or data-srcset
	imgSrcset := getAttr(imgNode, "srcset")
	imgDataSrcset := getAttr(imgNode, "data-srcset")
	var imgSrcsetCands []SrcsetCandidate
	if imgSrcset != "" {
		imgSrcsetCands = append(imgSrcsetCands, ParseSrcset(imgSrcset)...)
	}
	if imgDataSrcset != "" {
		imgSrcsetCands = append(imgSrcsetCands, ParseSrcset(imgDataSrcset)...)
	}
	SortSrcsetCandidatesDesc(imgSrcsetCands)
	for idx, sc := range imgSrcsetCands {
		if resolved := resolveURL(baseURL, sc.URL); resolved != nil {
			p := 70
			if idx > 0 {
				p = 68
			}
			candidates = append(candidates, &Candidate{
				RawURL:      sc.URL,
				ResolvedURL: resolved,
				Source:      SourceImgSrcset,
				Priority:    p,
				Descriptor:  sc.Descriptor,
			})
			if idx >= 2 {
				break
			}
		}
	}

	// 5. data-src / data-lazy / data-image
	lazyAttrs := []string{
		"data-src", "data-image", "data-image-url", "data-url",
		"data-lazy", "data-lazy-src",
	}
	for _, attrName := range lazyAttrs {
		val := getAttr(imgNode, attrName)
		if val != "" {
			if resolved := resolveURL(baseURL, val); resolved != nil {
				candidates = append(candidates, &Candidate{
					RawURL:      val,
					ResolvedURL: resolved,
					Source:      SourceDataSrc,
					Priority:    60,
				})
				break
			}
		}
	}

	// 6. standard img src
	src := getAttr(imgNode, "src")
	if src != "" {
		if resolved := resolveURL(baseURL, src); resolved != nil {
			candidates = append(candidates, &Candidate{
				RawURL:      src,
				ResolvedURL: resolved,
				Source:      SourceImgSrc,
				Priority:    50,
			})
		}
	}

	if len(candidates) == 0 {
		return nil
	}

	// Sort candidates by priority descending
	sortCandidatesByPriority(candidates)

	// Deduplicate candidates within the slot, keeping higher priority
	var unique []*Candidate
	seen := make(map[string]bool)
	for _, c := range candidates {
		uStr := c.ResolvedURL.String()
		if !seen[uStr] {
			seen[uStr] = true
			unique = append(unique, c)
		}
	}

	return &Slot{
		Candidates: unique,
		LandingURL: landingURL,
	}
}

// IsSameOrigin checks whether two URLs share the same scheme and hostname.
func IsSameOrigin(u1, u2 *url.URL) bool {
	if u1 == nil || u2 == nil {
		return false
	}
	return strings.EqualFold(u1.Scheme, u2.Scheme) && strings.EqualFold(u1.Hostname(), u2.Hostname())
}

func sortCandidatesByPriority(candidates []*Candidate) {
	for i := 0; i < len(candidates)-1; i++ {
		for j := i + 1; j < len(candidates); j++ {
			if candidates[j].Priority > candidates[i].Priority {
				candidates[i], candidates[j] = candidates[j], candidates[i]
			}
		}
	}
}

func processMetaTag(n *html.Node, baseURL *url.URL, addSlot func(*Slot)) {
	property := getAttr(n, "property")
	name := getAttr(n, "name")
	content := getAttr(n, "content")
	if content == "" {
		return
	}

	propOrName := strings.ToLower(property)
	if propOrName == "" {
		propOrName = strings.ToLower(name)
	}

	if propOrName == "og:image" || propOrName == "og:image:url" {
		if resolved := resolveURL(baseURL, content); resolved != nil {
			addSlot(&Slot{
				Candidates: []*Candidate{
					{
						RawURL:      content,
						ResolvedURL: resolved,
						Source:      SourceMetaOG,
						Priority:    35,
					},
				},
			})
		}
	} else if propOrName == "twitter:image" || propOrName == "twitter:image:src" {
		if resolved := resolveURL(baseURL, content); resolved != nil {
			addSlot(&Slot{
				Candidates: []*Candidate{
					{
						RawURL:      content,
						ResolvedURL: resolved,
						Source:      SourceMetaTwitter,
						Priority:    35,
					},
				},
			})
		}
	}
}

func processJSONLD(n *html.Node, baseURL *url.URL, addSlot func(*Slot)) {
	if n.FirstChild == nil || n.FirstChild.Type != html.TextNode {
		return
	}
	text := strings.TrimSpace(n.FirstChild.Data)
	if text == "" {
		return
	}

	var data any
	if err := json.Unmarshal([]byte(text), &data); err != nil {
		return
	}

	extractJSONLDSlots(data, baseURL, addSlot)
}

func extractJSONLDSlots(v any, baseURL *url.URL, addSlot func(*Slot)) {
	switch val := v.(type) {
	case []any:
		for _, item := range val {
			extractJSONLDSlots(item, baseURL, addSlot)
		}
	case map[string]any:
		// 1. Process @graph if present
		if graph, ok := val["@graph"]; ok {
			extractJSONLDSlots(graph, baseURL, addSlot)
		}

		// 2. Process explicit image fields
		if img, ok := val["image"]; ok {
			extractJSONLDImageField(img, baseURL, 30, addSlot)
		}
		if contentURL, ok := val["contentUrl"]; ok {
			extractJSONLDImageField(contentURL, baseURL, 32, addSlot)
		}
		if thumbURL, ok := val["thumbnailUrl"]; ok {
			extractJSONLDImageField(thumbURL, baseURL, 28, addSlot)
		}

		// 3. If this map is an ImageObject, check its "url" and "contentUrl" fields
		if isImageObjectType(val["@type"]) {
			if u, ok := val["url"]; ok {
				extractJSONLDImageField(u, baseURL, 32, addSlot)
			}
		}

		// 4. Recurse into nested objects or arrays only (skip string, number, bool values)
		for k, child := range val {
			switch k {
			case "@graph", "image", "contentUrl", "thumbnailUrl":
				continue
			}
			switch child.(type) {
			case map[string]any, []any:
				extractJSONLDSlots(child, baseURL, addSlot)
			}
		}
	}
}

func isImageObjectType(t any) bool {
	switch v := t.(type) {
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "ImageObject")
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && strings.EqualFold(strings.TrimSpace(s), "ImageObject") {
				return true
			}
		}
	}
	return false
}

func extractJSONLDImageField(v any, baseURL *url.URL, priority int, addSlot func(*Slot)) {
	switch val := v.(type) {
	case string:
		raw := strings.TrimSpace(val)
		if raw != "" {
			if resolved := resolveURL(baseURL, raw); resolved != nil {
				addSlot(&Slot{
					Candidates: []*Candidate{
						{
							RawURL:      raw,
							ResolvedURL: resolved,
							Source:      SourceJSONLD,
							Priority:    priority,
						},
					},
				})
			}
		}
	case []any:
		for _, item := range val {
			extractJSONLDImageField(item, baseURL, priority, addSlot)
		}
	case map[string]any:
		extractJSONLDSlots(val, baseURL, addSlot)
	}
}

func extractCSSURLs(cssText string, baseURL *url.URL, source CandidateSource, priority int, addSlot func(*Slot)) {
	matches := cssURLRegex.FindAllStringSubmatch(cssText, -1)
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		raw := strings.TrimSpace(match[1])
		raw = strings.Trim(raw, `"'`)
		if resolved := resolveURL(baseURL, raw); resolved != nil {
			addSlot(&Slot{
				Candidates: []*Candidate{
					{
						RawURL:      raw,
						ResolvedURL: resolved,
						Source:      source,
						Priority:    priority,
					},
				},
			})
		}
	}
}

func resolveURL(baseURL *url.URL, raw string) *url.URL {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	// Ignore data URIs
	if strings.HasPrefix(strings.ToLower(raw), "data:") {
		return nil
	}

	ref, err := url.Parse(raw)
	if err != nil {
		return nil
	}

	resolved := baseURL.ResolveReference(ref)
	// Fragment (#...) is not part of HTTP resource identity
	resolved.Fragment = ""

	// Accept only http and https
	scheme := strings.ToLower(resolved.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil
	}

	return resolved
}

func isDirectRasterURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return imageinfo.IsRasterExtension(path.Ext(u.Path))
}

func getAttr(n *html.Node, key string) string {
	for _, attr := range n.Attr {
		if strings.EqualFold(attr.Key, key) {
			return strings.TrimSpace(attr.Val)
		}
	}
	return ""
}
