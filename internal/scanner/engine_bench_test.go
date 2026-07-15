package scanner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

const benchFileSize = 1 << 20

// benchUserFileRoot mirrors userFileTestRoot for benchmarks: the working
// directory is used because the system temporary directory sits under the
// protected AppData tree.
func benchUserFileRoot(b *testing.B) string {
	b.Helper()
	root, err := os.MkdirTemp(".", "scanner-bench-")
	if err != nil {
		b.Fatalf("MkdirTemp failed: %v", err)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		b.Fatalf("Abs failed: %v", err)
	}
	b.Cleanup(func() {
		_ = os.RemoveAll(absRoot)
	})
	return absRoot
}

func writeBenchFile(b *testing.B, path string, data []byte) {
	b.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		b.Fatalf("WriteFile failed: %v", err)
	}
}

// BenchmarkScanDuplicatePairs measures the staged pipeline on a corpus where
// every file has an exact duplicate, so boundary hashing and full SHA-256
// confirmation both run for the whole corpus.
func BenchmarkScanDuplicatePairs(b *testing.B) {
	root := benchUserFileRoot(b)
	const pairs = 8
	var totalBytes int64
	for i := 0; i < pairs; i++ {
		payload := bytes.Repeat([]byte{byte(i + 1)}, benchFileSize)
		writeBenchFile(b, filepath.Join(root, fmt.Sprintf("left-%d.bin", i)), payload)
		writeBenchFile(b, filepath.Join(root, fmt.Sprintf("right-%d.bin", i)), payload)
		totalBytes += 2 * benchFileSize
	}

	engine := NewEngine(4)
	b.SetBytes(totalBytes)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		groups, err := engine.Scan(context.Background(), []string{root}, make(chan Progress, 1024))
		if err != nil {
			b.Fatalf("Scan returned error: %v", err)
		}
		if len(groups) != pairs {
			b.Fatalf("expected %d duplicate groups, got %d", pairs, len(groups))
		}
	}
}

// BenchmarkScanBoundaryCollisions measures the worst case for staged
// hashing: files that share size and 4 KiB boundaries but differ in the
// middle, forcing a full streaming hash that confirms no duplicates.
func BenchmarkScanBoundaryCollisions(b *testing.B) {
	root := benchUserFileRoot(b)
	const files = 16
	var totalBytes int64
	for i := 0; i < files; i++ {
		payload := bytes.Repeat([]byte{0xAB}, benchFileSize)
		payload[benchFileSize/2] = byte(i)
		writeBenchFile(b, filepath.Join(root, fmt.Sprintf("collision-%d.bin", i)), payload)
		totalBytes += benchFileSize
	}

	engine := NewEngine(4)
	b.SetBytes(totalBytes)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		groups, err := engine.Scan(context.Background(), []string{root}, make(chan Progress, 1024))
		if err != nil {
			b.Fatalf("Scan returned error: %v", err)
		}
		if len(groups) != 0 {
			b.Fatalf("expected no duplicate groups, got %d", len(groups))
		}
	}
}
