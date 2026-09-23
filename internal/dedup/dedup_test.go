package dedup

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func TestURLTracker(t *testing.T) {
	tracker := NewURLTracker()

	url1 := "https://example.com/photo.jpg"
	url2 := "https://example.com/photo.jpg"
	url3 := "https://example.com/other.png"

	if !tracker.Add(url1) {
		t.Errorf("expected first Add(%q) to be true", url1)
	}

	if tracker.Add(url2) {
		t.Errorf("expected duplicate Add(%q) to be false", url2)
	}

	if !tracker.Seen(url1) {
		t.Errorf("expected Seen(%q) to be true", url1)
	}

	if !tracker.Add(url3) {
		t.Errorf("expected Add(%q) to be true", url3)
	}
}

func TestContentTracker(t *testing.T) {
	tracker := NewContentTracker()

	hash1 := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	file1 := "empty.jpg"
	file2 := "empty_copy.jpg"

	existing, isDup := tracker.Register(hash1, file1)
	if isDup {
		t.Errorf("first register should not be duplicate")
	}
	if existing != "" {
		t.Errorf("expected empty existing filename, got %s", existing)
	}

	existing, isDup = tracker.Register(hash1, file2)
	if !isDup {
		t.Errorf("second register of same hash should be duplicate")
	}
	if existing != file1 {
		t.Errorf("expected existing %s, got %s", file1, existing)
	}
}

func TestComputeFileSHA256(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gleanomess-sha-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	testFile := filepath.Join(tmpDir, "test.bin")
	content := []byte("hello gleanomess world")
	if err := os.WriteFile(testFile, content, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	hash, err := ComputeFileSHA256(testFile)
	if err != nil {
		t.Fatalf("ComputeFileSHA256 failed: %v", err)
	}

	// echo -n "hello gleanomess world" | sha256sum
	expected := "2be607de6713a3d0190be44ff7b26a7aee9e6e29549c1471fa36703424598dc4"
	if hash != expected {
		t.Errorf("expected %s, got %s", expected, hash)
	}
}

func TestContentTracker_FinalizeConcurrency(t *testing.T) {
	tracker := NewContentTracker()
	hash := "a1b2c3d4e5f67890"

	const concurrency = 50
	var (
		saveCount  int32
		dupCount   int32
		firstCount int32
		wg         sync.WaitGroup
	)

	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func(id int) {
			defer wg.Done()

			filename, isDup, err := tracker.Finalize(hash, func() (string, error) {
				atomic.AddInt32(&saveCount, 1)
				return "unique_image.png", nil
			})
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if isDup {
				atomic.AddInt32(&dupCount, 1)
				if filename != "unique_image.png" {
					t.Errorf("expected duplicate to return first filename 'unique_image.png', got %q", filename)
				}
			} else {
				atomic.AddInt32(&firstCount, 1)
			}
		}(i)
	}

	wg.Wait()

	if saveCount != 1 {
		t.Errorf("expected saveFn to be called exactly 1 time, got %d", saveCount)
	}
	if firstCount != 1 {
		t.Errorf("expected exactly 1 worker to get isDup=false, got %d", firstCount)
	}
	if dupCount != concurrency-1 {
		t.Errorf("expected %d workers to get isDup=true, got %d", concurrency-1, dupCount)
	}
}

func TestContentTracker_FinalizeRollbackOnError(t *testing.T) {
	tracker := NewContentTracker()
	hash := "fail_then_succeed_hash"

	// 1. Finalize fails with error
	expectedErr := os.ErrPermission
	filename, isDup, err := tracker.Finalize(hash, func() (string, error) {
		return "", expectedErr
	})
	if err != expectedErr {
		t.Fatalf("expected error %v, got %v", expectedErr, err)
	}
	if isDup {
		t.Errorf("isDup should be false on failure")
	}
	if filename != "" {
		t.Errorf("filename should be empty on failure")
	}

	// 2. Check that hash was NOT registered
	if _, ok := tracker.Check(hash); ok {
		t.Errorf("hash should not be registered after failed save")
	}

	// 3. Second attempt succeeds and registers hash
	filename, isDup, err = tracker.Finalize(hash, func() (string, error) {
		return "recovered.png", nil
	})
	if err != nil {
		t.Fatalf("unexpected error on second attempt: %v", err)
	}
	if isDup {
		t.Errorf("isDup should be false on successful first save")
	}
	if filename != "recovered.png" {
		t.Errorf("expected 'recovered.png', got %q", filename)
	}

	// 4. Subsequent check returns the registered filename
	if existing, ok := tracker.Check(hash); !ok || existing != "recovered.png" {
		t.Errorf("expected Check to return 'recovered.png', got %q (ok=%v)", existing, ok)
	}
}
