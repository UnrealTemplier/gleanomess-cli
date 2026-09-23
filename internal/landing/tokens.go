package landing

import (
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode"

	"github.com/UnrealTemplier/gleanomess-cli/internal/scraper"
)

var (
	tokenSplitRegex = regexp.MustCompile(`[^a-zA-Z0-9_\-]+`)

	stopWords = map[string]bool{
		"photo": true, "photos": true, "image": true, "images": true,
		"picture": true, "pictures": true, "gallery": true, "galleries": true,
		"album": true, "albums": true, "item": true, "items": true,
		"media": true, "asset": true, "assets": true, "detail": true,
		"view": true, "full": true, "large": true, "medium": true,
		"small": true, "thumb": true, "thumbs": true, "thumbnail": true,
		"thumbnails": true, "mini": true, "frame": true, "framed": true,
		"preview": true, "original": true, "scaled": true, "master": true,
		"standard": true, "secure": true, "token": true, "hash": true,
		"width": true, "height": true, "size": true, "index": true,
		"default": true, "page": true, "pages": true, "next": true,
		"prev": true, "previous": true, "first": true, "last": true,
		"show": true, "display": true, "download": true, "upload": true,
		"uploads": true, "content": true, "static": true, "public": true,
		"file": true, "files": true, "attachment": true, "pic": true,
		"pics": true, "html": true, "htm": true, "php": true, "asp": true,
	}

	commonDimensions = map[string]bool{
		"100": true, "150": true, "200": true, "250": true, "256": true,
		"300": true, "400": true, "500": true, "600": true, "800": true,
		"1024": true, "1080": true, "1200": true, "1280": true, "1600": true,
		"1920": true, "2048": true, "2560": true, "3840": true, "4096": true,
	}
)

// ExtractTokens extracts conservative identifier tokens from a URL.
func ExtractTokens(u *url.URL) []string {
	if u == nil {
		return nil
	}

	tokenSet := make(map[string]bool)

	// 1. Process Path
	pathStr := u.Path
	if unescaped, err := url.PathUnescape(pathStr); err == nil {
		pathStr = unescaped
	}

	// Strip common raster or html extensions from filename
	base := path.Base(pathStr)
	ext := path.Ext(base)
	if ext != "" {
		baseWithoutExt := strings.TrimSuffix(base, ext)
		addCleanToken(baseWithoutExt, tokenSet)
	}

	// Split full path into tokens
	parts := tokenSplitRegex.Split(pathStr, -1)
	for _, p := range parts {
		addCleanToken(p, tokenSet)
	}

	// 2. Process Query parameters
	for k, vals := range u.Query() {
		// Ignore common pagination/view/dimension query keys
		kLower := strings.ToLower(k)
		if kLower == "page" || kLower == "view" || kLower == "w" || kLower == "h" || kLower == "width" || kLower == "height" {
			continue
		}
		for _, v := range vals {
			queryParts := tokenSplitRegex.Split(v, -1)
			for _, qp := range queryParts {
				addCleanToken(qp, tokenSet)
			}
		}
	}

	var result []string
	for t := range tokenSet {
		result = append(result, t)
	}
	return result
}

func addCleanToken(raw string, set map[string]bool) {
	tok := strings.ToLower(strings.TrimSpace(raw))
	tok = strings.Trim(tok, "-_.")
	if tok == "" {
		return
	}

	// Check stop words
	if stopWords[tok] {
		return
	}

	// If purely numeric:
	isNum := true
	for _, r := range tok {
		if !unicode.IsDigit(r) {
			isNum = false
			break
		}
	}

	if isNum {
		// Purely numeric tokens must have >= 4 digits and not be common screen dimensions
		if len(tok) >= 4 && !commonDimensions[tok] {
			set[tok] = true
		}
		return
	}

	// Alphanumeric tokens: must be >= 4 chars, have at least one digit or hyphen/underscore
	if len(tok) >= 4 {
		hasDigitOrSep := strings.ContainsAny(tok, "0123456789-_")
		if hasDigitOrSep {
			set[tok] = true
		}
	}
}

// ExtractSlotTokens extracts identifier tokens from all source signals in an image slot.
func ExtractSlotTokens(slot *scraper.Slot) map[string]bool {
	tokens := make(map[string]bool)
	if slot == nil {
		return tokens
	}

	if slot.LandingURL != nil {
		for _, t := range ExtractTokens(slot.LandingURL) {
			tokens[t] = true
		}
	}

	for _, cand := range slot.Candidates {
		if cand.ResolvedURL != nil {
			for _, t := range ExtractTokens(cand.ResolvedURL) {
				tokens[t] = true
			}
		}
	}

	return tokens
}

// MatchScore calculates how many identifier tokens match between sourceTokens and a candidate URL.
func MatchScore(sourceTokens map[string]bool, candURL *url.URL) int {
	if len(sourceTokens) == 0 || candURL == nil {
		return 0
	}
	candTokens := ExtractTokens(candURL)
	score := 0
	for _, ct := range candTokens {
		if sourceTokens[ct] {
			score++
		}
	}
	return score
}
