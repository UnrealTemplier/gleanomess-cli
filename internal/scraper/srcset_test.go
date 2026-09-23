package scraper

import (
	"testing"
)

func TestParseSrcset(t *testing.T) {
	t.Run("Width descriptors", func(t *testing.T) {
		input := "small.jpg 400w, medium.jpg 800w, large.jpg 1600w"
		candidates := ParseSrcset(input)
		if len(candidates) != 3 {
			t.Fatalf("expected 3 candidates, got %d", len(candidates))
		}
		best, ok := SelectBestSrcsetCandidate(candidates)
		if !ok {
			t.Fatal("expected candidate to be found")
		}
		if best.URL != "large.jpg" || best.Width != 1600 {
			t.Errorf("expected large.jpg 1600w, got %s %d", best.URL, best.Width)
		}
	})

	t.Run("Density descriptors", func(t *testing.T) {
		input := "image.jpg 1x, image@2x.jpg 2x, image@3x.jpg 3x"
		candidates := ParseSrcset(input)
		if len(candidates) != 3 {
			t.Fatalf("expected 3 candidates, got %d", len(candidates))
		}
		best, ok := SelectBestSrcsetCandidate(candidates)
		if !ok {
			t.Fatal("expected candidate to be found")
		}
		if best.URL != "image@3x.jpg" || best.Density != 3.0 {
			t.Errorf("expected image@3x.jpg 3x, got %s %f", best.URL, best.Density)
		}
	})

	t.Run("Ignore data URIs", func(t *testing.T) {
		input := "data:image/png;base64,iVBORw0KGgo= 100w, real.jpg 500w"
		candidates := ParseSrcset(input)
		if len(candidates) != 1 {
			t.Fatalf("expected 1 candidate, got %d", len(candidates))
		}
		if candidates[0].URL != "real.jpg" {
			t.Errorf("expected real.jpg, got %s", candidates[0].URL)
		}
	})
}
