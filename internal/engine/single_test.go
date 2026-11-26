package engine

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/amemiya02/nimget/internal/download"
)

func TestSingleKeepsStreamingProbeResponse(t *testing.T) {
	data := randomData(t, 96<<10)
	srv := &fileServer{data: data, noRanges: true, piece: 8 << 10, pause: 60 * time.Millisecond}
	server := srv.start(t)
	cfg := testConfig()
	cfg.StallTimeout = 300 * time.Millisecond
	dest := filepath.Join(t.TempDir(), "out.bin")
	if err := run(t, cfg, server.URL, dest); err != nil {
		t.Fatal(err)
	}
	requireFile(t, dest, data)
	if got := srv.requests.Load(); got != 1 {
		t.Fatalf("sent %d requests for a complete probe response; want 1", got)
	}
}

func TestSingleRetriesWhenProbeBodyStalls(t *testing.T) {
	data := randomData(t, 32<<10)
	srv := &fileServer{data: data, noRanges: true, etag: `"stable"`}
	srv.fault = func(w http.ResponseWriter, r *http.Request, _, _, request int64) bool {
		if request != 1 {
			if got := r.Header.Get("If-Match"); got != srv.etag {
				t.Errorf("retry If-Match = %q; want %s", got, srv.etag)
			}
			return false
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data[:1024])
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		return true
	}
	server := srv.start(t)
	cfg := testConfig()
	cfg.StallTimeout = 200 * time.Millisecond
	cfg.MaxAttempts = 1
	dest := filepath.Join(t.TempDir(), "out.bin")
	if err := run(t, cfg, server.URL, dest); err != nil {
		t.Fatal(err)
	}
	requireFile(t, dest, data)
	if got := srv.requests.Load(); got != 2 {
		t.Fatalf("sent %d requests after one body stalled; want 2", got)
	}
}

func TestSingleDoesNotReusePartialBodySentAs200(t *testing.T) {
	data := []byte("complete body")
	srv := &fileServer{data: data, noRanges: true}
	srv.fault = func(w http.ResponseWriter, r *http.Request, _, _, _ int64) bool {
		if r.Header.Get("Range") == "" {
			return false
		}
		w.Header().Set("Content-Range", "bytes 0-0/"+strconv.Itoa(len(data)))
		w.Header().Set("Content-Length", "1")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data[:1])
		return true
	}
	server := srv.start(t)
	dest := filepath.Join(t.TempDir(), "out.bin")
	if err := run(t, testConfig(), server.URL, dest); err != nil {
		t.Fatal(err)
	}
	requireFile(t, dest, data)
}

func TestSingleFallsBackWhenProbeHasNoBody(t *testing.T) {
	data := []byte("complete body")
	srv := &fileServer{data: data, noRanges: true}
	srv.fault = func(w http.ResponseWriter, r *http.Request, _, _, _ int64) bool {
		if r.Header.Get("Range") == "" {
			return false
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(http.StatusOK)
		return true
	}
	server := srv.start(t)
	cfg := testConfig()
	cfg.MaxAttempts = 1
	dest := filepath.Join(t.TempDir(), "out.bin")
	if err := run(t, cfg, server.URL, dest); err != nil {
		t.Fatal(err)
	}
	requireFile(t, dest, data)
	if got := srv.requests.Load(); got != 2 {
		t.Fatalf("sent %d requests after the server omitted the probe body; want 2", got)
	}
}

type probeTransport func(*http.Request) (*http.Response, error)

func (f probeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type countedBody struct {
	*strings.Reader
	closes int
}

func (b *countedBody) Close() error {
	b.closes++
	return nil
}

func TestFullProbeResponseOwnership(t *testing.T) {
	for _, mode := range []string{"inspect", "dispatch rejects DASH", "consume"} {
		t.Run(mode, func(t *testing.T) {
			body := &countedBody{Reader: strings.NewReader("bytes")}
			var requestCtx context.Context
			cfg := testConfig()
			cfg.ProbeClient = &http.Client{Transport: probeTransport(func(r *http.Request) (*http.Response, error) {
				if requestCtx != nil {
					t.Fatal("unexpected second request")
				}
				requestCtx = r.Context()
				headers := make(http.Header)
				if mode == "dispatch rejects DASH" {
					headers.Set("Content-Type", "application/dash+xml")
				}
				return &http.Response{StatusCode: 200, ContentLength: 5, Header: headers, Request: r, Body: body}, nil
			})}
			engine, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			const url = "https://example.test/file"
			dest := filepath.Join(t.TempDir(), "output")
			switch mode {
			case "inspect":
				_, err = engine.Inspect(t.Context(), url)
			case "dispatch rejects DASH":
				err = engine.Download(t.Context(), url, dest, nil)
				if !errors.Is(err, download.ErrUnsupportedMedia) {
					t.Fatalf("DASH error = %v", err)
				}
			case "consume":
				err = engine.Download(t.Context(), url, dest, nil)
				requireFile(t, dest, []byte("bytes"))
			}
			if mode != "dispatch rejects DASH" && err != nil {
				t.Fatal(err)
			}
			if body.closes != 1 || requestCtx.Err() == nil {
				t.Fatalf("closes = %d, request error = %v", body.closes, requestCtx.Err())
			}
			if mode != "consume" && body.Len() != 5 {
				t.Fatal("unused probe body was read")
			}
		})
	}
}
