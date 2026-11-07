package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
)

// The official quantized GGUF files are published by Preferred Networks at
// https://huggingface.co/pfnet/plamo-3-translate-0.8b-GGUF. Q4_K_M is the
// recommended default (best accuracy/size balance per the model card).
const (
	plamoGGUFRepo = "pfnet/plamo-3-translate-0.8b-GGUF"
	plamoBaseName = "plamo-3-translate-0.8b"
)

// plamoQuantizations lists the quantization types shipped in the official
// GGUF repository.
var plamoQuantizations = []string{"Q4_K_M", "Q4_0", "Q3_K_M", "Q2_K"}

// modelDownloadURL returns the Hugging Face URL that serves the official GGUF
// file named by modelPath, or "" when the file name is not an official
// release (in which case it cannot be auto-downloaded).
func modelDownloadURL(modelPath string) string {
	name := filepath.Base(modelPath)
	for _, q := range plamoQuantizations {
		if name == plamoBaseName+"-"+q+".gguf" {
			return fmt.Sprintf("https://huggingface.co/%s/resolve/main/%s", plamoGGUFRepo, name)
		}
	}
	return ""
}

// ensureModel makes sure the model file exists, downloading it from Hugging
// Face when it is missing and its name matches an official release. It is a
// no-op when the file is already present. Set HF_TOKEN when a token is
// required to access the repository.
func ensureModel(ctx context.Context, modelPath string) error {
	if _, err := os.Stat(modelPath); err == nil {
		return nil
	}
	url := modelDownloadURL(modelPath)
	if url == "" {
		return fmt.Errorf("model file not found: %s (no automatic download available; run `nix run .#download-model` or place the file manually)", modelPath)
	}
	log.Printf("model file not found: %s; downloading from %s", modelPath, url)
	if err := downloadFile(ctx, url, modelPath); err != nil {
		return fmt.Errorf("model download failed: %w", err)
	}
	return nil
}

// authHeaders adds the optional Hugging Face token from HF_TOKEN.
func authHeaders(h http.Header) {
	if token := os.Getenv("HF_TOKEN"); token != "" {
		h.Set("Authorization", "Bearer "+token)
	}
}

// downloadAttempts caps automatic retries; a surviving .part is kept so the
// next run (or scripts/download_model.py) can resume.
const downloadAttempts = 3

// downloadFile streams url to dest with resume support: partial data is kept
// in dest+".part" and resumed via a Range request until the file is complete,
// then atomically renamed. Mirrors scripts/download_model.py.
func downloadFile(ctx context.Context, url, dest string) error {
	expected, err := remoteSize(ctx, url)
	if err != nil {
		return err
	}
	part := dest + ".part"
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	for attempt := 1; ; attempt++ {
		done, err := downloadFileOnce(ctx, url, part, expected)
		if err == nil {
			return os.Rename(part, dest)
		}
		if done || attempt >= downloadAttempts {
			return err
		}
		var se *stalePartError
		if errors.As(err, &se) {
			_ = os.Remove(part)
		}
		log.Printf("model download attempt %d failed (%v); retrying", attempt, err)
	}
}

// stalePartError signals that the .part file is unusable and a restart from
// byte zero may succeed.
type stalePartError struct{ err error }

func (e *stalePartError) Error() string { return e.err.Error() }

// remoteSize resolves the final content length of url, following redirects.
func remoteSize(ctx context.Context, url string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return 0, err
	}
	authHeaders(req.Header)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HEAD %s: unexpected status %d", url, resp.StatusCode)
	}
	length := resp.Header.Get("Content-Length")
	if length == "" {
		return 0, fmt.Errorf("HEAD %s: no Content-Length", url)
	}
	return strconv.ParseInt(length, 10, 64)
}

// downloadFileOnce appends url's content to part, resuming from its current
// size. It reports whether the failure is final (done=true means do not
// retry).
func downloadFileOnce(ctx context.Context, url, part string, expected int64) (done bool, err error) {
	var offset int64
	if fi, statErr := os.Stat(part); statErr == nil {
		offset = fi.Size()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return true, err
	}
	authHeaders(req.Header)
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		return false, &stalePartError{err: fmt.Errorf("stale partial download of %s", url)}
	case resp.StatusCode == http.StatusPartialContent && offset > 0:
		// resumed
	case resp.StatusCode == http.StatusOK:
		offset = 0 // server ignored the Range header; start over
	default:
		return true, fmt.Errorf("GET %s: unexpected status %d", url, resp.StatusCode)
	}
	flags := os.O_CREATE | os.O_WRONLY
	if offset > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return true, err
	}
	progress := newProgress(offset, expected)
	downloaded := offset
	copyErr := func() error {
		buf := make([]byte, 1<<20)
		for {
			n, readErr := resp.Body.Read(buf)
			if n > 0 {
				if _, writeErr := f.Write(buf[:n]); writeErr != nil {
					return writeErr
				}
				downloaded += int64(n)
				progress.step(downloaded)
			}
			if readErr == io.EOF {
				return nil
			}
			if readErr != nil {
				return readErr
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
	}()
	closeErr := f.Close()
	if copyErr != nil {
		return false, copyErr
	}
	if closeErr != nil {
		return false, closeErr
	}
	progress.step(downloaded)
	if downloaded != expected {
		return false, fmt.Errorf("download incomplete (%d / %d bytes)", downloaded, expected)
	}
	return false, nil
}

// progress logs download progress every ~5%.
type progress struct {
	expected int64
	last     int
	started  bool
}

func newProgress(offset, expected int64) *progress {
	p := &progress{expected: expected}
	p.last = int(offset * 100 / max(expected, 1))
	return p
}
func (p *progress) step(downloaded int64) {
	pct := int(downloaded * 100 / max(p.expected, 1))
	if pct/5 != p.last/5 || !p.started {
		p.started = true
		p.last = pct
		log.Printf("model download: %.2f / %.2f GB (%d%%)",
			float64(downloaded)/1e9, float64(p.expected)/1e9, pct)
	}
}
