package scraper

import (
	"strconv"
	"strings"
)

// SrcsetCandidate represents a single entry in a srcset attribute.
type SrcsetCandidate struct {
	URL        string
	Width      int
	Density    float64
	Descriptor string
}

// ParseSrcset parses an HTML srcset or data-srcset attribute value.
// It returns all valid candidates.
func ParseSrcset(srcset string) []SrcsetCandidate {
	var candidates []SrcsetCandidate
	srcset = strings.TrimSpace(srcset)
	if srcset == "" {
		return candidates
	}

	// Split candidates by comma, taking care not to split inside URLs if any
	rawEntries := splitSrcsetEntries(srcset)
	for _, raw := range rawEntries {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}

		fields := strings.Fields(raw)
		if len(fields) == 0 {
			continue
		}

		urlStr := fields[0]
		// Ignore data URIs
		if strings.HasPrefix(strings.ToLower(urlStr), "data:") {
			continue
		}

		cand := SrcsetCandidate{
			URL: urlStr,
		}

		if len(fields) > 1 {
			desc := strings.TrimSpace(fields[1])
			cand.Descriptor = desc
			if strings.HasSuffix(desc, "w") || strings.HasSuffix(desc, "W") {
				valStr := desc[:len(desc)-1]
				if w, err := strconv.Atoi(valStr); err == nil && w > 0 {
					cand.Width = w
				}
			} else if strings.HasSuffix(desc, "x") || strings.HasSuffix(desc, "X") {
				valStr := desc[:len(desc)-1]
				if d, err := strconv.ParseFloat(valStr, 64); err == nil && d > 0 {
					cand.Density = d
				}
			}
		}

		candidates = append(candidates, cand)
	}

	return candidates
}

// SelectBestSrcsetCandidate selects the highest quality candidate:
// 1. If any width descriptors exist, pick the candidate with the largest width.
// 2. Otherwise if density descriptors exist, pick the candidate with the largest density.
// 3. Otherwise pick the last candidate.
func SelectBestSrcsetCandidate(candidates []SrcsetCandidate) (SrcsetCandidate, bool) {
	if len(candidates) == 0 {
		return SrcsetCandidate{}, false
	}

	hasWidth := false
	for _, c := range candidates {
		if c.Width > 0 {
			hasWidth = true
			break
		}
	}

	if hasWidth {
		best := candidates[0]
		for _, c := range candidates[1:] {
			if c.Width > best.Width {
				best = c
			}
		}
		return best, true
	}

	hasDensity := false
	for _, c := range candidates {
		if c.Density > 0 {
			hasDensity = true
			break
		}
	}

	if hasDensity {
		best := candidates[0]
		for _, c := range candidates[1:] {
			if c.Density > best.Density {
				best = c
			}
		}
		return best, true
	}

	// Fallback to the last candidate
	return candidates[len(candidates)-1], true
}

func splitSrcsetEntries(s string) []string {
	var entries []string
	n := len(s)
	i := 0

	for i < n {
		// Skip leading whitespace and separator commas
		for i < n && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r' || s[i] == ',') {
			i++
		}
		if i >= n {
			break
		}

		start := i
		inParen := 0
		for i < n {
			if s[i] == '(' {
				inParen++
			} else if s[i] == ')' && inParen > 0 {
				inParen--
			} else if s[i] == ',' && inParen == 0 {
				beforeHasSpace := strings.ContainsAny(s[start:i], " \t\r\n")
				afterIsSpace := i+1 < n && (s[i+1] == ' ' || s[i+1] == '\t' || s[i+1] == '\r' || s[i+1] == '\n')
				if beforeHasSpace || afterIsSpace {
					break
				}
			}
			i++
		}

		entry := strings.TrimSpace(s[start:i])
		if entry != "" {
			entries = append(entries, entry)
		}
		if i < n && s[i] == ',' {
			i++
		}
	}
	return entries
}
