package scraper

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

var (
	paginationClassOrIDRegex = regexp.MustCompile(`(?i)(pagination|pager|paginate|page-nav|pagenav|page_nav|page-links|pages|paging|expp-container|navigation)`)
)

// FindNextPage scans doc for a conservative, same-origin next-page link relative to currentURL.
// Priority order:
//  1. <link rel="next" href="..."> in <head>
//  2. <a rel="next" href="...">
//  3. <a aria-label="Next" ...> or <a title="Next" ...>
//  4. Obvious text next link (e.g. "Next", ":: next ::", "Следующая", "Вперёд") ONLY if in explicit pagination context.
//
// Returns the resolved URL without fragment, or nil if no valid next page link is found.
func FindNextPage(doc *html.Node, currentURL *url.URL) *url.URL {
	if doc == nil || currentURL == nil {
		return nil
	}

	baseURL := *currentURL
	findBaseHref(doc, &baseURL)

	// 1. <link rel="next" href="..."> in <head>
	if nextURL := findLinkRelNext(doc, &baseURL, currentURL); nextURL != nil {
		return nextURL
	}

	// Collect all <a> nodes
	var anchorNodes []*html.Node
	var collectAnchors func(*html.Node)
	collectAnchors = func(n *html.Node) {
		if n.Type == html.ElementNode && strings.EqualFold(n.Data, "a") {
			anchorNodes = append(anchorNodes, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			collectAnchors(c)
		}
	}
	collectAnchors(doc)

	// 2. <a rel="next" href="...">
	for _, a := range anchorNodes {
		rel := getAttr(a, "rel")
		if hasRelNextToken(rel) {
			if u := validateNextHref(a, &baseURL, currentURL); u != nil {
				return u
			}
		}
	}

	// 3. <a aria-label="Next" ...> or <a title="Next" ...>
	for _, a := range anchorNodes {
		aria := getAttr(a, "aria-label")
		title := getAttr(a, "title")
		if isNextLabelOrTitle(aria) || isNextLabelOrTitle(title) {
			if u := validateNextHref(a, &baseURL, currentURL); u != nil {
				return u
			}
		}
	}

	// 4. Obvious text next link in explicit pagination context
	for _, a := range anchorNodes {
		text := extractNodeText(a)
		if isNextTextKeyword(text) {
			if isInPaginationContext(a) {
				if u := validateNextHref(a, &baseURL, currentURL); u != nil {
					return u
				}
			}
		}
	}

	return nil
}

func findLinkRelNext(n *html.Node, baseURL, currentURL *url.URL) *url.URL {
	if n == nil {
		return nil
	}
	if n.Type == html.ElementNode && strings.EqualFold(n.Data, "link") {
		rel := getAttr(n, "rel")
		if hasRelNextToken(rel) {
			if u := validateNextHref(n, baseURL, currentURL); u != nil {
				return u
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if res := findLinkRelNext(c, baseURL, currentURL); res != nil {
			return res
		}
	}
	return nil
}

func hasRelNextToken(rel string) bool {
	fields := strings.Fields(strings.ToLower(rel))
	for _, f := range fields {
		if f == "next" {
			return true
		}
	}
	return false
}

func isNextLabelOrTitle(val string) bool {
	cleaned := strings.ToLower(strings.TrimSpace(val))
	switch cleaned {
	case "next", "next page", "next-page", "следующая", "следующая страница", "вперёд", "вперед":
		return true
	default:
		return false
	}
}

func isNextTextKeyword(text string) bool {
	// Strip decoration punctuation like ':', '>', '»', '→', '[', ']', '|', '-'
	cleaned := strings.Trim(text, " \t\r\n:;>»→[]|-_~")
	cleaned = strings.ToLower(strings.TrimSpace(cleaned))

	switch cleaned {
	case "next", "next page", "следующая", "следующая страница", "вперёд", "вперед":
		return true
	default:
		return false
	}
}

func isInPaginationContext(aNode *html.Node) bool {
	// 1. Check ancestors up to 5 levels
	depth := 0
	for p := aNode.Parent; p != nil && depth < 5; p = p.Parent {
		depth++
		if p.Type == html.ElementNode {
			if strings.EqualFold(p.Data, "nav") {
				return true
			}
			classVal := getAttr(p, "class")
			idVal := getAttr(p, "id")
			if paginationClassOrIDRegex.MatchString(classVal) || paginationClassOrIDRegex.MatchString(idVal) {
				return true
			}
		}
	}

	// 2. Check siblings in parent and grandparent for numeric page links
	if hasNumericSibling(aNode.Parent) {
		return true
	}
	if aNode.Parent != nil && hasNumericSibling(aNode.Parent.Parent) {
		return true
	}

	return false
}

func hasNumericSibling(parent *html.Node) bool {
	if parent == nil {
		return false
	}
	for c := parent.FirstChild; c != nil; c = c.NextSibling {
		txt := strings.TrimSpace(extractNodeText(c))
		if txt != "" {
			if num, err := strconv.Atoi(txt); err == nil && num > 0 && num < 10000 {
				return true
			}
		}
	}
	return false
}

func extractNodeText(n *html.Node) string {
	if n == nil {
		return ""
	}
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(curr *html.Node) {
		if curr.Type == html.TextNode {
			sb.WriteString(curr.Data)
		}
		for c := curr.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return sb.String()
}

func validateNextHref(node *html.Node, baseURL, currentURL *url.URL) *url.URL {
	rawHref := strings.TrimSpace(getAttr(node, "href"))
	if rawHref == "" || rawHref == "#" || strings.HasPrefix(rawHref, "javascript:") {
		return nil
	}

	resolved := resolveURL(baseURL, rawHref)
	if resolved == nil {
		return nil
	}

	// Strip fragment
	resolved.Fragment = ""

	// Must be same origin
	if !IsSameOrigin(currentURL, resolved) {
		return nil
	}

	// Scheme must be http or https
	if resolved.Scheme != "http" && resolved.Scheme != "https" {
		return nil
	}

	// Must be different from current URL (ignoring fragment)
	cleanCurrent := *currentURL
	cleanCurrent.Fragment = ""
	if resolved.String() == cleanCurrent.String() {
		return nil
	}

	return resolved
}
