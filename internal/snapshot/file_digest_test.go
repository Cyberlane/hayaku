package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFileDigestExplicitLimitPreservesStreaming(t *testing.T) {
	name := filepath.Join(t.TempDir(), "large-tool")
	file, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	const size = 70 << 20
	if err := file.Truncate(size); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	// Independent raw SHA-256 reference for sparse zero bytes. No filesystem
	// framing, metadata or shared digest helper supplies the expected identity.
	reference := sha256.New()
	zeros := make([]byte, 4096)
	for n := 0; n < size; n += len(zeros) {
		_, _ = reference.Write(zeros)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got, err := FileDigest(context.Background(), name, 256<<20)
	runtime.ReadMemStats(&after)
	if err != nil || got != hex.EncodeToString(reference.Sum(nil)) {
		t.Fatal("large tool raw identity mismatch", err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 8<<20 {
		t.Fatalf("streaming tool identity allocated %d bytes", allocated)
	}
	if _, err := FileDigest(context.Background(), name, 64<<20); err == nil {
		t.Fatal("explicit smaller file limit ignored")
	}
	for _, limit := range []int64{-1, 0, maxTree + 1} {
		if _, err := FileDigest(context.Background(), name, limit); err == nil {
			t.Fatal("invalid digest limit accepted")
		}
	}
}
