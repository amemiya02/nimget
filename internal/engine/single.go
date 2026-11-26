package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/amemiya02/nimget/internal/logging"
)

// singleStream owns a full response returned by the probe until the first
// read takes it. Dispatch can abandon the stream before a file is opened.
type singleStream struct {
	first *streamResponse
}

func (s *singleStream) close() {
	if s.first != nil {
		s.first.close()
	}
}

type streamResponse struct {
	resp   *http.Response
	ctx    context.Context
	cancel context.CancelCauseFunc
	stall  *stallTimer
}

func newStreamResponse(ctx context.Context) *streamResponse {
	reqCtx, cancel := context.WithCancelCause(ctx)
	return &streamResponse{ctx: reqCtx, cancel: cancel}
}

func (r *streamResponse) close() {
	if r.stall != nil {
		r.stall.Stop()
	}
	r.cancel(nil)
	if r.resp != nil {
		_ = r.resp.Body.Close()
	}
}

// open takes the retained body once. Every later attempt starts a fresh GET.
func (s *singleStream) open(ctx context.Context, j *job, etag string) (*streamResponse, *attemptError) {
	timeout := j.engine.cfg.StallTimeout
	response := s.first
	if response == nil {
		response = newStreamResponse(ctx)
	}
	s.first = nil
	response.stall = newStallTimer(timeout, func() { response.cancel(ErrStalled) })
	if response.resp != nil {
		response.stall.Reset(timeout)
		return response, nil
	}

	req, err := j.engine.newRequest(response.stall.trace(response.ctx), j.url)
	if err != nil {
		response.close()
		return nil, fatal(err)
	}
	if etag != "" {
		req.Header.Set("If-Match", etag)
	}
	resp, err := j.engine.cfg.ProbeClient.Do(req)
	if err != nil {
		failure := interruptedStream(ctx, response.ctx, logging.HTTPError(err))
		response.close()
		return nil, failure
	}
	response.resp = resp
	return response, nil
}

func interruptedStream(ctx, reqCtx context.Context, err error) *attemptError {
	if errors.Is(context.Cause(reqCtx), ErrStalled) && ctx.Err() == nil {
		err = ErrStalled
	}
	return retryable(fmt.Errorf("download: %w", err))
}

// single downloads over one stream, using the probe body when the server
// ignored Range. Each attempt restarts from zero.
func (j *job) single(ctx context.Context, info probeInfo, stream *singleStream) error {
	e := j.engine
	if err := j.discard(); err != nil {
		return err
	}
	if info.size > 0 {
		if free, err := freeSpace(filepath.Dir(j.part)); err == nil && free < info.size+freeSpaceMargin {
			return fmt.Errorf("%w: need %d bytes plus margin, %d available", ErrInsufficientSpace, info.size, free)
		}
	}
	f, _, err := j.createPartial(checkpoint{Version: checkpointVersion, Identity: j.identity})
	if err != nil {
		return fmt.Errorf("create partial file: %w", err)
	}
	defer f.Close()
	if stream.first != nil {
		// A server can advertise the full size on the Range probe but leave
		// its body incomplete. A failed probe body does not consume any of
		// the ordinary GET attempts.
		first := j.singleOnce(ctx, info, f, stream)
		if first == nil {
			return nil
		}
		if !first.retryable {
			return first
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	var last *attemptError
	for attempt := 0; attempt < e.cfg.MaxAttempts; attempt++ {
		if last != nil {
			if err := sleepBeforeRetry(ctx, attempt-1, last.retryAfter, e.cfg.BaseBackoff, e.cfg.MaxBackoff); err != nil {
				return err
			}
		}
		last = j.singleOnce(ctx, info, f, stream)
		if last == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !last.retryable {
			break
		}
	}
	return last
}

func (j *job) singleOnce(ctx context.Context, info probeInfo, f *os.File, stream *singleStream) *attemptError {
	e := j.engine
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fatal(fmt.Errorf("seek partial file: %w", err))
	}
	if err := f.Truncate(0); err != nil {
		return fatal(fmt.Errorf("truncate partial file: %w", err))
	}
	j.progress.completed.Store(0)
	j.progress.total.Store(info.size)
	if info.size == 0 {
		return closeSynced(f)
	}

	response, aerr := stream.open(ctx, j, info.etag)
	if aerr != nil {
		return aerr
	}
	defer response.close()
	resp := response.resp
	if err := identityEncoding(resp); err != nil {
		return fatal(err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusPreconditionFailed:
		return fatal(ErrSourceChanged)
	default:
		return statusError("download", resp)
	}
	if info.etag != "" && strongETag(resp.Header.Get("ETag")) != info.etag {
		return fatal(ErrSourceChanged)
	}
	total := resp.ContentLength
	j.progress.total.Store(total)

	buf := make([]byte, bufferSize)
	var written int64
	for {
		n, readErr := resp.Body.Read(buf[:e.limit(bufferSize)])
		if n > 0 {
			response.stall.Stop()
			if err := e.wait(ctx, n); err != nil {
				return interruptedStream(ctx, response.ctx, err)
			}
			if _, err := f.Write(buf[:n]); err != nil {
				return fatal(fmt.Errorf("write partial file: %w", err))
			}
			response.stall.Reset(e.cfg.StallTimeout)
			written += int64(n)
			j.progress.completed.Store(written)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return interruptedStream(ctx, response.ctx, readErr)
		}
	}
	if total >= 0 && written != total {
		return retryable(fmt.Errorf("short response: got %d bytes, want %d", written, total))
	}
	j.progress.total.Store(written)
	return closeSynced(f)
}

func closeSynced(f *os.File) *attemptError {
	if err := f.Sync(); err != nil {
		return fatal(fmt.Errorf("sync partial file: %w", err))
	}
	if err := f.Close(); err != nil {
		return fatal(fmt.Errorf("close partial file: %w", err))
	}
	return nil
}
