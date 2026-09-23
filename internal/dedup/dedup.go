package dedup

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"sync"
)

// URLTracker manages deduplication of normalized URLs.
type URLTracker struct {
	mu   sync.Mutex
	urls map[string]bool
}

// NewURLTracker creates a thread-safe URL deduplication tracker.
func NewURLTracker() *URLTracker {
	return &URLTracker{
		urls: make(map[string]bool),
	}
}

// Add returns true if the URL was not previously seen, false if it's a duplicate.
func (u *URLTracker) Add(normalizedURL string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.urls[normalizedURL] {
		return false
	}
	u.urls[normalizedURL] = true
	return true
}

// Seen checks if a normalized URL was already recorded.
func (u *URLTracker) Seen(normalizedURL string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.urls[normalizedURL]
}

// ContentTracker manages streaming SHA-256 deduplication of downloaded file contents.
type ContentTracker struct {
	mu     sync.Mutex
	hashes map[string]string // sha256Hex -> savedFilename
}

// NewContentTracker creates a thread-safe content deduplication tracker.
func NewContentTracker() *ContentTracker {
	return &ContentTracker{
		hashes: make(map[string]string),
	}
}

// Register checks if a hash is already known.
// If it is known, returns (existingFilename, true).
// If not known, registers hash -> savedFilename and returns ("", false).
func (c *ContentTracker) Register(hashHex, savedFilename string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.hashes[hashHex]; ok {
		return existing, true
	}
	c.hashes[hashHex] = savedFilename
	return "", false
}

// Check returns whether the hash is already known, without registering.
func (c *ContentTracker) Check(hashHex string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	existing, ok := c.hashes[hashHex]
	return existing, ok
}

// Finalize atomically checks whether hashHex is already registered and, if not, executes
// the saveFn callback to allocate and store the file into the output directory.
//
// If the hash is already registered: saveFn is NOT called, and Finalize returns (existingFilename, true, nil).
// If saveFn returns an error: hashHex is NOT registered in the tracker, and the error is returned.
// If saveFn succeeds: hashHex is registered with the returned finalFilename, and Finalize returns (finalFilename, false, nil).
//
// This guarantees that concurrent workers processing identical content will serialize only during
// the brief finalization/rename step, preventing race conditions and ensuring that at most one final file is saved.
func (c *ContentTracker) Finalize(hashHex string, saveFn func() (string, error)) (string, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if existing, ok := c.hashes[hashHex]; ok {
		return existing, true, nil
	}

	finalFilename, err := saveFn()
	if err != nil {
		return "", false, err
	}

	c.hashes[hashHex] = finalFilename
	return finalFilename, false, nil
}

// ComputeFileSHA256 streams a file from disk through sha256 without reading all of it into memory.
func ComputeFileSHA256(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
