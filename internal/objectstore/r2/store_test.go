package r2

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"gitslice.io/gitslice/internal/storage"
)

func TestResolveObjectKey(t *testing.T) {
	tests := []struct {
		name    string
		prefix  string
		key     string
		want    string
		wantErr bool
	}{
		{
			name: "plain key",
			key:  "blobs/sha256/ab/cd/hash",
			want: "blobs/sha256/ab/cd/hash",
		},
		{
			name:   "prefix",
			prefix: "repo-objects",
			key:    "blobs/hash",
			want:   "repo-objects/blobs/hash",
		},
		{
			name:   "prefix with slashes",
			prefix: "/repo-objects/",
			key:    "blobs/hash",
			want:   "repo-objects/blobs/hash",
		},
		{
			name:   "nested prefix is cleaned",
			prefix: "repo//objects/",
			key:    "trees//sha256/hash.json",
			want:   "repo/objects/trees/sha256/hash.json",
		},
		{
			name:   "single leading slash matches filesystem store",
			prefix: "repo",
			key:    "/blobs/hash",
			want:   "repo/blobs/hash",
		},
		{
			name:    "empty key rejected",
			key:     "",
			wantErr: true,
		},
		{
			name:    "dot key rejected",
			key:     ".",
			wantErr: true,
		},
		{
			name:    "parent key rejected",
			key:     "..",
			wantErr: true,
		},
		{
			name:    "parent-prefixed key rejected",
			key:     "../blob",
			wantErr: true,
		},
		{
			name:    "parent after clean rejected",
			key:     "blob/../../other",
			wantErr: true,
		},
		{
			name:    "absolute key rejected after one leading slash is stripped",
			key:     "//blobs/hash",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveObjectKey(tt.prefix, tt.key)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveObjectKey(%q, %q) error = nil, want error", tt.prefix, tt.key)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveObjectKey(%q, %q) error = %v", tt.prefix, tt.key, err)
			}
			if got != tt.want {
				t.Fatalf("resolveObjectKey(%q, %q) = %q, want %q", tt.prefix, tt.key, got, tt.want)
			}
		})
	}
}

func TestRangeHeader(t *testing.T) {
	tests := []struct {
		name    string
		offset  int64
		length  int64
		want    string
		wantErr bool
	}{
		{name: "full object", want: ""},
		{name: "offset only", offset: 5, want: "bytes=5-"},
		{name: "length only", length: 10, want: "bytes=0-9"},
		{name: "offset and length", offset: 5, length: 10, want: "bytes=5-14"},
		{name: "negative offset treated as zero", offset: -5, length: 10, want: "bytes=0-9"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := rangeHeader(tt.offset, tt.length)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("rangeHeader(%d, %d) error = nil, want error", tt.offset, tt.length)
				}
				return
			}
			if err != nil {
				t.Fatalf("rangeHeader(%d, %d) error = %v", tt.offset, tt.length, err)
			}
			if got != tt.want {
				t.Fatalf("rangeHeader(%d, %d) = %q, want %q", tt.offset, tt.length, got, tt.want)
			}
		})
	}
}

func TestPutStreamsReadSeekerWithContentLengthAndCopyUsesCopyObject(t *testing.T) {
	var gotMethod, gotPath, gotCopySource string
	var gotContentLength int64
	var gotBody []byte
	return404 := false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotCopySource = r.Header.Get("X-Amz-Copy-Source")
		gotContentLength, _ = strconv.ParseInt(r.Header.Get("Content-Length"), 10, 64)
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		if return404 {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code><Message>missing</Message></Error>`))
			return
		}
		if gotCopySource != "" {
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><CopyObjectResult><ETag>"abc"</ETag></CopyObjectResult>`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store, err := New(Config{
		Endpoint:        srv.URL,
		Region:          "auto",
		Bucket:          "gitslice-test",
		Prefix:          "env",
		AccessKeyID:     "test-key",
		SecretAccessKey: "test-secret",
		UsePathStyle:    true,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// Put via *os.File (io.ReadSeeker) sends explicit Content-Length without chunking.
	tmpPath := filepath.Join(t.TempDir(), "staged.bin")
	payload := bytes.Repeat([]byte("abcd"), 256)
	if err := os.WriteFile(tmpPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(tmpPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if err := store.Put(context.Background(), "staging/u1", f); err != nil {
		t.Fatalf("Put(*os.File) error = %v", err)
	}
	if gotMethod != http.MethodPut || gotPath != "/gitslice-test/env/staging/u1" {
		t.Fatalf("Put request = %s %s", gotMethod, gotPath)
	}
	if gotContentLength != int64(len(payload)) {
		t.Fatalf("Content-Length = %d, want %d", gotContentLength, len(payload))
	}
	if !bytes.Equal(gotBody, payload) {
		t.Fatalf("Put body mismatch: got %d bytes, want %d", len(gotBody), len(payload))
	}

	// Copy sends server-side X-Amz-Copy-Source with encoded source key.
	if err := store.Copy(context.Background(), "blobs/sha256/ab/cd/abcd", "staging/u 1:2"); err != nil {
		t.Fatalf("Copy() error = %v", err)
	}
	if gotPath != "/gitslice-test/env/blobs/sha256/ab/cd/abcd" {
		t.Fatalf("Copy target path = %q", gotPath)
	}
	if want := "gitslice-test/env/staging/u%201:2"; gotCopySource != want {
		t.Fatalf("X-Amz-Copy-Source = %q, want %q", gotCopySource, want)
	}

	// Copy missing source maps to storage.ErrNotFound.
	return404 = true
	if err := store.Copy(context.Background(), "blobs/dst", "staging/missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Copy(missing) error = %v, want storage.ErrNotFound", err)
	}
}
