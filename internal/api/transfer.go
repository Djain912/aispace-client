package api

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

// ErrTransferStalled means an upload or download made no byte progress before
// the inactivity timer expired.
var ErrTransferStalled = errors.New("transfer stalled")

type transferWatch struct {
	mu      sync.Mutex
	timeout time.Duration
	timer   *time.Timer
	cancel  context.CancelCauseFunc
	active  bool
	stopped bool
}

func startTransferWatch(parent context.Context, timeout time.Duration) (context.Context, *transferWatch) {
	if timeout <= 0 {
		return parent, nil
	}
	ctx, cancel := context.WithCancelCause(parent)
	w := &transferWatch{timeout: timeout, cancel: cancel}
	return ctx, w
}

func (w *transferWatch) touch() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return
	}
	w.active = true
	if w.timer == nil {
		w.timer = time.AfterFunc(w.timeout, w.expire)
	} else {
		w.timer.Stop()
		w.timer.Reset(w.timeout)
	}
}

func (w *transferWatch) pause() {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.active = false
	if w.timer != nil {
		w.timer.Stop()
	}
	w.mu.Unlock()
}

func (w *transferWatch) expire() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.stopped && w.active {
		w.active = false
		w.cancel(ErrTransferStalled)
	}
}

func (w *transferWatch) stop() {
	if w == nil {
		return
	}
	w.mu.Lock()
	if !w.stopped {
		w.stopped = true
		w.active = false
		if w.timer != nil {
			w.timer.Stop()
		}
		w.cancel(nil)
	}
	w.mu.Unlock()
}

type progressReader struct {
	r     io.Reader
	watch *transferWatch
}

func (r *progressReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		r.watch.touch()
	}
	if err != nil {
		r.watch.pause()
	}
	return n, err
}

type watchedBody struct {
	io.ReadCloser
	ctx   context.Context
	watch *transferWatch
}

func (b *watchedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.watch.touch()
	}
	if err != nil {
		b.watch.pause()
		if errors.Is(context.Cause(b.ctx), ErrTransferStalled) {
			return n, ErrTransferStalled
		}
	}
	return n, err
}

func (b *watchedBody) Close() error {
	b.watch.stop()
	return b.ReadCloser.Close()
}

func stalledError() *Error {
	return &Error{Code: "timeout", Message: "transfer stalled without byte progress", cause: ErrTransferStalled}
}
