package blob

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestLocalStoreConformance runs the cross-adapter contract against the local filesystem
// backend (always available — no external dependency).
func TestLocalStoreConformance(t *testing.T) {
	RunStoreConformance(t, func(t *testing.T) Store {
		s, err := NewLocalStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return s
	})
}

// TestS3StoreConformance runs the SAME contract against a real S3-compatible backend when
// one is configured (a MinIO container in CI / dev). It skips otherwise, mirroring the
// ClickHouse integration tests — the contract is the interface; this proves the S3 adapter
// honors it identically to the local one.
//
// Configure with:
//
//	LLMOBS_BLOB_S3_TEST_ENDPOINT=localhost:9000
//	LLMOBS_BLOB_S3_TEST_ACCESS=minioadmin
//	LLMOBS_BLOB_S3_TEST_SECRET=minioadmin
//	LLMOBS_BLOB_S3_TEST_BUCKET=llmobs-blobs-test
func TestS3StoreConformance(t *testing.T) {
	endpoint := os.Getenv("LLMOBS_BLOB_S3_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("LLMOBS_BLOB_S3_TEST_ENDPOINT unset — skipping real-S3 blob conformance")
	}
	cfg := S3Config{
		Endpoint:  endpoint,
		AccessKey: os.Getenv("LLMOBS_BLOB_S3_TEST_ACCESS"),
		SecretKey: os.Getenv("LLMOBS_BLOB_S3_TEST_SECRET"),
		Bucket:    os.Getenv("LLMOBS_BLOB_S3_TEST_BUCKET"),
		UseSSL:    strings.EqualFold(os.Getenv("LLMOBS_BLOB_S3_TEST_SSL"), "true"),
	}
	// One shared store across subtests: each subtest uses distinct DeriveKey'd keys, and
	// the contract includes delete/overwrite, so there is no cross-subtest bleed.
	store, err := NewS3Store(context.Background(), cfg)
	if err != nil {
		t.Fatalf("connect S3 test backend: %v", err)
	}
	RunStoreConformance(t, func(t *testing.T) Store { return store })
}
