package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/riverqueue/river"
)

// defaultClient sends the requests when Options.Client is nil.
var defaultClient = &http.Client{CheckRedirect: keepMethod}

// keepMethod follows the redirects that send the POST on as it is (307 and
// 308). Go turns a POST into a GET on any other, which would deliver
// nothing and look like success; those answers come back as they are, and
// refuse the transcription.
func keepMethod(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	switch req.Response.StatusCode {
	case http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return nil
	}
	return http.ErrUseLastResponse
}

// worker sends one transcription per job.
type worker struct {
	river.WorkerDefaults[args]
	client    *http.Client
	userAgent string
	timeout   time.Duration
	onFailure func(Failure)
	log       *slog.Logger
}

// Work implements river.Worker. A returned error has River retry the job,
// unless it is the last attempt or a JobCancel.
func (w *worker) Work(ctx context.Context, job *river.Job[args]) error {
	t := job.Args.Transcription
	host := hostOf(job.Args.URL)
	err := w.post(ctx, job.Args)
	if err == nil {
		w.log.Debug("transcription sent to the webhook", "transcription", t.ID, "host", host, "attempt", job.Attempt)
		return nil
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		// Goscriber is quitting. River puts the job back, without counting
		// the attempt, when the error wraps the cancellation.
		return fmt.Errorf("interrupted: %w", ctx.Err())
	}
	if errors.Is(err, context.DeadlineExceeded) {
		err = fmt.Errorf("no answer within %v", w.timeout)
	}
	refused := errors.Is(err, errRefused)
	last := refused || job.Attempt >= job.MaxAttempts
	w.log.Warn("the transcription could not be sent to the webhook", "transcription", t.ID, "host", host,
		"attempt", job.Attempt, "of", job.MaxAttempts, "retry", !last, "err", err)
	if last && w.onFailure != nil {
		w.onFailure(Failure{ID: t.ID, Host: host, Attempts: job.Attempt, Err: err})
	}
	if refused {
		return river.JobCancel(err)
	}
	return err
}

// errRefused marks an answer that sending again cannot change.
var errRefused = errors.New("refused")

// statusError is an answer outside 2xx.
type statusError struct {
	status string // such as "404 Not Found"
	body   string // the start of the answer's body, if it has one
	// refused reports that sending again cannot change the answer.
	refused bool
}

func (e *statusError) Error() string {
	msg := "the server answered " + e.status
	if e.body != "" {
		msg += ": " + e.body
	}
	return msg
}

func (e *statusError) Is(target error) bool { return target == errRefused && e.refused }

// post sends the transcription once.
func (w *worker) post(ctx context.Context, a args) error {
	body, err := json.Marshal(a.Transcription)
	if err != nil {
		return fmt.Errorf("%w: %w", errRefused, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%w: %w", errRefused, withoutURL(err))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", strconv.FormatInt(a.Transcription.ID, 10))
	if w.userAgent != "" {
		req.Header.Set("User-Agent", w.userAgent)
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return withoutURL(err)
	}
	defer resp.Body.Close()
	start := readStart(resp.Body)
	switch c := resp.StatusCode; {
	case c >= 200 && c < 300:
		return nil
	case c == http.StatusRequestTimeout, c == http.StatusTooManyRequests, c >= 500:
		return &statusError{status: resp.Status, body: start}
	default:
		return &statusError{status: resp.Status, body: start, refused: true}
	}
}

// withoutURL drops the URL that net/http puts in its errors: it may hold a
// token, and the errors reach the log and the notices.
func withoutURL(err error) error {
	if uerr, ok := errors.AsType[*url.Error](err); ok {
		return uerr.Err
	}
	return err
}

// readStart reads an answer's body, so the connection can be reused, and
// returns its first line, cut to 200 bytes, for the messages.
func readStart(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 64<<10))
	_, _ = io.Copy(io.Discard, io.LimitReader(r, 1<<20))
	s, _, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		s = s[:200]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
		s += "…"
	}
	return s
}

// hostOf is u's host, the part of the URL safe to show.
func hostOf(u string) string {
	if p, err := url.Parse(u); err == nil && p.Host != "" {
		return p.Host
	}
	return "the webhook"
}
