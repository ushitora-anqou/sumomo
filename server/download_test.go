package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
)

func TestModelDownloadURL(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{
			"models/plamo-3-translate-0.8b-Q4_K_M.gguf",
			"https://huggingface.co/pfnet/plamo-3-translate-0.8b-GGUF/resolve/main/plamo-3-translate-0.8b-Q4_K_M.gguf",
		},
		{
			"models/plamo-3-translate-0.8b-Q2_K.gguf",
			"https://huggingface.co/pfnet/plamo-3-translate-0.8b-GGUF/resolve/main/plamo-3-translate-0.8b-Q2_K.gguf",
		},
		{"/models/custom-model.gguf", ""},
		{"models/plamo-3-translate-0.8b-Q8_0.gguf", ""}, // not an official quantization
	}
	for _, tt := range tests {
		if got := modelDownloadURL(tt.path); got != tt.want {
			t.Errorf("modelDownloadURL(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestEnsureModelExistingIsNoOp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plamo-3-translate-0.8b-Q4_K_M.gguf")
	if err := os.WriteFile(path, []byte("model"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureModel(context.Background(), path); err != nil {
		t.Errorf("ensureModel() error for an existing file: %v", err)
	}
}

func TestEnsureModelUnknownNameFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom.gguf")
	if err := ensureModel(context.Background(), path); err == nil {
		t.Error("ensureModel() succeeded for a non-official model name, want an error")
	}
}

// serveModel serves content with HEAD/GET and Range support, counting
// Range-served GETs.
func serveModel(t *testing.T, content []byte) (*httptest.Server, *int32) {
	t.Helper()
	var rangeGets int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			w.Header().Set("Content-Length", strconv.Itoa(len(content)))
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			if rng := r.Header.Get("Range"); rng != "" {
				var start int
				if _, err := fmt.Sscanf(rng, "bytes=%d-", &start); err != nil || start > len(content) {
					w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
					return
				}
				atomic.AddInt32(&rangeGets, 1)
				w.Header().Set("Content-Length", strconv.Itoa(len(content)-start))
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(content)-1, len(content)))
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(content[start:])
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(content)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(content)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &rangeGets
}

func TestDownloadFile(t *testing.T) {
	content := []byte("fake gguf payload for testing")
	srv, rangeGets := serveModel(t, content)
	dest := filepath.Join(t.TempDir(), "nested", "model.gguf")

	if err := downloadFile(context.Background(), srv.URL, dest); err != nil {
		t.Fatalf("downloadFile() error: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Errorf("downloaded %q, want %q", got, content)
	}
	if n := atomic.LoadInt32(rangeGets); n != 0 {
		t.Errorf("%d Range requests for a fresh download, want 0", n)
	}
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Error("the .part file was not cleaned up")
	}
}

func TestDownloadFileResumesPartial(t *testing.T) {
	content := []byte("0123456789abcdef")
	srv, rangeGets := serveModel(t, content)
	dest := filepath.Join(t.TempDir(), "model.gguf")

	// A partial download exists: the first 8 bytes are already on disk.
	if err := os.WriteFile(dest+".part", content[:8], 0o644); err != nil {
		t.Fatal(err)
	}
	if err := downloadFile(context.Background(), srv.URL, dest); err != nil {
		t.Fatalf("downloadFile() error: %v", err)
	}
	if n := atomic.LoadInt32(rangeGets); n != 1 {
		t.Errorf("%d Range requests, want 1 (resume)", n)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Errorf("downloaded %q, want %q", got, content)
	}
}

func TestDownloadFileRecoversFromStalePart(t *testing.T) {
	content := []byte("short")
	srv, _ := serveModel(t, content)
	dest := filepath.Join(t.TempDir(), "model.gguf")

	// The .part is larger than the remote file: the server must answer 416
	// and the download must restart from scratch.
	stale := make([]byte, 100)
	if err := os.WriteFile(dest+".part", stale, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := downloadFile(context.Background(), srv.URL, dest); err != nil {
		t.Fatalf("downloadFile() error: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Errorf("downloaded %q, want %q", got, content)
	}
}

func TestDownloadFileCancelledContext(t *testing.T) {
	content := []byte("payload")
	srv, _ := serveModel(t, content)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dest := filepath.Join(t.TempDir(), "model.gguf")
	if err := downloadFile(ctx, srv.URL, dest); err == nil {
		t.Error("downloadFile() succeeded with a cancelled context, want an error")
	}
}
