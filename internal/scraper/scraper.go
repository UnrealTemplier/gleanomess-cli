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
)

// Candidate represents a discovered image asset candidate.
type Candidate struct {
	RawURL      string
	ResolvedURL *url.URL
	Source      CandidateSource
	Priority    int
	Descriptor  string // e.g. "1600w" or "2x"
}

var cssURLRegex = regexp.MustCompile(`(?i)url\(\s*(?:['"]?)([^'")]+)(?:['"]?)\s*\)`)

// Scrape extracts ranked and deduplicated image candidates from an HTML document.
func Scrape(r io.Reader, pageURL *url.URL) ([]*Candidate, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, err
	}

	baseURL := *pageURL

	var candidates []*Candidate
	seenURLs := make(map[string]*Candidate)

	addCandidate := func(cand *Candidate) {
		if cand == nil || cand.ResolvedURL == nil {
			return
		}
		uStr := cand.ResolvedURL.String()
		if existing, ok := seenURLs[uStr]; ok {
			if cand.Priority > existing.Priority {
				seenURLs[uStr] = cand
			}
			return
		}
		seenURLs[uStr] = cand
		candidates = append(candidates, cand)
	}

	// 1. Look for <base href="..."> in head
	findBaseHref(doc, &baseURL)

	// 2. Traverse DOM and collect image slots and elements
	traverseDOM(doc, &baseURL, addCandidate)

	// Return ordered unique candidates
	var result []*Candidate
	for _, cand := range candidates {
		if seenURLs[cand.ResolvedURL.String()] == cand {
			result = append(result, cand)
		}
	}
	return result, nil
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

func traverseDOM(n *html.Node, baseURL *url.URL, addCandidate func(*Candidate)) {
	walk(n, baseURL, nil, nil, addCandidate)
}

func walk(
	n *html.Node,
	baseURL *url.URL,
	currAnchor *anchorContext,
	currPicture *pictureContext,
	addCandidate func(*Candidate),
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
				extractCSSURLs(attr.Val, baseURL, SourceCSSInline, 20, addCandidate)
			}
		}

		switch tag {
		case "a":
			href := getAttr(n, "href")
			if href != "" && isDirectRasterURL(href) {
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
			cand := processImageSlot(n, baseURL, currAnchor, currPicture)
			if cand != nil {
				addCandidate(cand)
				if currAnchor != nil && cand.Source == SourceAnchorImage {
					currAnchor.consumed = true
				}
			}

		case "meta":
			processMetaTag(n, baseURL, addCandidate)

		case "script":
			typeVal := getAttr(n, "type")
			if strings.EqualFold(typeVal, "application/ld+json") {
				processJSONLD(n, baseURL, addCandidate)
			}

		case "style":
			if n.FirstChild != nil && n.FirstChild.Type == html.TextNode {
				extractCSSURLs(n.FirstChild.Data, baseURL, SourceCSSBlock, 20, addCandidate)
			}
		}
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c, baseURL, nextAnchor, nextPicture, addCandidate)
	}

	// On exiting <a>, if it pointed to a direct raster image and was not consumed by an inner <img>,
	// emit it as a standalone anchor candidate.
	if n.Type == html.ElementNode && strings.ToLower(n.Data) == "a" && nextAnchor != nil && !nextAnchor.consumed {
		if resolved := resolveURL(baseURL, nextAnchor.rawHref); resolved != nil {
			addCandidate(&Candidate{
				RawURL:      nextAnchor.rawHref,
				ResolvedURL: resolved,
				Source:      SourceStandaloneA,
				Priority:    40,
			})
		}
	}
}

func processImageSlot(
	imgNode *html.Node,
	baseURL *url.URL,
	currAnchor *anchorContext,
	currPicture *pictureContext,
) *Candidate {
	var candidates []*Candidate

	// 1. Wrapping <a href> pointing to a raster image (highest priority)
	if currAnchor != nil && currAnchor.rawHref != "" {
		if resolved := resolveURL(baseURL, currAnchor.rawHref); resolved != nil {
			candidates = append(candidates, &Candidate{
				RawURL:      currAnchor.rawHref,
				ResolvedURL: resolved,
				Source:      SourceAnchorImage,
				Priority:    100,
			})
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
		if best, ok := SelectBestSrcsetCandidate(allSourceCands); ok {
			if resolved := resolveURL(baseURL, best.URL); resolved != nil {
				candidates = append(candidates, &Candidate{
					RawURL:      best.URL,
					ResolvedURL: resolved,
					Source:      SourcePictureSource,
					Priority:    80,
					Descriptor:  best.Descriptor,
				})
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
	if best, ok := SelectBestSrcsetCandidate(imgSrcsetCands); ok {
		if resolved := resolveURL(baseURL, best.URL); resolved != nil {
			candidates = append(candidates, &Candidate{
				RawURL:      best.URL,
				ResolvedURL: resolved,
				Source:      SourceImgSrcset,
				Priority:    70,
				Descriptor:  best.Descriptor,
			})
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

	// Pick the candidate with the highest priority
	if len(candidates) == 0 {
		return nil
	}
	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.Priority > best.Priority {
			best = c
		}
	}
	return best
}

func processMetaTag(n *html.Node, baseURL *url.URL, addCandidate func(*Candidate)) {
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
			addCandidate(&Candidate{
				RawURL:      content,
				ResolvedURL: resolved,
				Source:      SourceMetaOG,
				Priority:    35,
			})
		}
	} else if propOrName == "twitter:image" || propOrName == "twitter:image:src" {
		if resolved := resolveURL(baseURL, content); resolved != nil {
			addCandidate(&Candidate{
				RawURL:      content,
				ResolvedURL: resolved,
				Source:      SourceMetaTwitter,
				Priority:    35,
			})
		}
	}
}

func processJSONLD(n *html.Node, baseURL *url.URL, addCandidate func(*Candidate)) {
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

	extractJSONLDValues(data, baseURL, addCandidate)
}

func extractJSONLDValues(v any, baseURL *url.URL, addCandidate func(*Candidate)) {
	switch val := v.(type) {
	case map[string]any:
		// Check @graph array
		if graph, ok := val["@graph"]; ok {
			extractJSONLDValues(graph, baseURL, addCandidate)
		}

		// Check contentUrl
		if cURL, ok := val["contentUrl"].(string); ok && cURL != "" {
			if resolved := resolveURL(baseURL, cURL); resolved != nil {
				addCandidate(&Candidate{
					RawURL:      cURL,
					ResolvedURL: resolved,
					Source:      SourceJSONLD,
					Priority:    32,
				})
			}
		}

		// Check thumbnailUrl
		if tURL, ok := val["thumbnailUrl"].(string); ok && tURL != "" {
			if resolved := resolveURL(baseURL, tURL); resolved != nil {
				addCandidate(&Candidate{
					RawURL:      tURL,
					ResolvedURL: resolved,
					Source:      SourceJSONLD,
					Priority:    28,
				})
			}
		}

		// Check image field
		if imgField, ok := val["image"]; ok {
			switch img := imgField.(type) {
			case string:
				if resolved := resolveURL(baseURL, img); resolved != nil {
					addCandidate(&Candidate{
						RawURL:      img,
						ResolvedURL: resolved,
						Source:      SourceJSONLD,
						Priority:    30,
					})
				}
			default:
				extractJSONLDValues(img, baseURL, addCandidate)
			}
		}

		// If this is an ImageObject with url field
		if typeStr, ok := val["@type"].(string); ok && strings.EqualFold(typeStr, "ImageObject") {
			if u, ok := val["url"].(string); ok && u != "" {
				if resolved := resolveURL(baseURL, u); resolved != nil {
					addCandidate(&Candidate{
						RawURL:      u,
						ResolvedURL: resolved,
						Source:      SourceJSONLD,
						Priority:    32,
					})
				}
			}
		}

		// Recursively check all map elements
		for _, v := range val {
			extractJSONLDValues(v, baseURL, addCandidate)
		}

	case string:
		if resolved := resolveURL(baseURL, val); resolved != nil {
			addCandidate(&Candidate{
				RawURL:      val,
				ResolvedURL: resolved,
				Source:      SourceJSONLD,
				Priority:    30,
			})
		}

	case []any:
		for _, item := range val {
			extractJSONLDValues(item, baseURL, addCandidate)
		}
	}
}

func extractCSSURLs(cssText string, baseURL *url.URL, source CandidateSource, priority int, addCandidate func(*Candidate)) {
	matches := cssURLRegex.FindAllStringSubmatch(cssText, -1)
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		raw := strings.TrimSpace(match[1])
		raw = strings.Trim(raw, `"'`)
		if resolved := resolveURL(baseURL, raw); resolved != nil {
			addCandidate(&Candidate{
				RawURL:      raw,
				ResolvedURL: resolved,
				Source:      source,
				Priority:    priority,
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
