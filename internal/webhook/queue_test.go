package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/opragmatico/goscriber/internal/config"
)

var ctx = context.Background()

// receiver is a webhook that answers each request with the next status of
// a script, then with the last one, and keeps what it received.
type receiver struct {
	*httptest.Server
	mu       sync.Mutex
	statuses []int
	got      []request
	// hold, when set, keeps each request waiting until it is closed or the
	// request is cancelled.
	hold chan struct{}
	// arrived gets a value as each request arrives.
	arrived chan struct{}
}

// request is what a receiver got.
type request struct {
	method, path, contentType, webhookID, userAgent, authorization string
	body                                                           []byte
}

func newReceiver(t *testing.T, statuses ...int) *receiver {
	t.Helper()
	r := &receiver{statuses: statuses, arrived: make(chan struct{}, 100)}
	r.Server = httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(r.Close)
	return r
}

func (r *receiver) serve(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.got = append(r.got, request{
		method: req.Method, path: req.URL.Path, contentType: req.Header.Get("Content-Type"),
		webhookID: req.Header.Get("webhook-id"), userAgent: req.Header.Get("User-Agent"), authorization: req.Header.Get("Authorization"), body: body,
	})
	status := http.StatusOK
	if len(r.statuses) > 0 {
		status = r.statuses[0]
		if len(r.statuses) > 1 {
			r.statuses = r.statuses[1:]
		}
	}
	hold := r.hold
	r.mu.Unlock()
	r.arrived <- struct{}{}
	if hold != nil {
		select {
		case <-hold:
		case <-req.Context().Done():
			return
		}
	}
	w.WriteHeader(status)
	if status >= 300 {
		_, _ = io.WriteString(w, "nope\nsecond line")
	}
}

func (r *receiver) requests() []request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.got)
}

// failures collects what OnFailure hears.
type failures struct {
	mu  sync.Mutex
	got []Failure
}

func (f *failures) add(x Failure) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, x)
}

func (f *failures) list() []Failure {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.got)
}

// testOptions retry at once, poll often and give up after 3 attempts.
func testOptions(f *failures) Options {
	return Options{
		UserAgent:      "Goscriber/test",
		OnFailure:      f.add,
		Log:            slog.New(slog.DiscardHandler),
		maxAttempts:    3,
		attemptTimeout: 2 * time.Second,
		pollInterval:   20 * time.Millisecond,
		retryDelay:     time.Millisecond,
	}
}

func openQueue(t *testing.T, path string, opts Options) *Queue {
	t.Helper()
	q, err := Open(ctx, path, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = q.Close(ctx) })
	return q
}

func startQueue(t *testing.T, opts Options) *Queue {
	t.Helper()
	q := openQueue(t, filepath.Join(t.TempDir(), "jobs.sqlite3"), opts)
	if err := q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return q
}

var started = time.Date(2026, 11, 20, 9, 30, 15, 123456789, time.FixedZone("CET", 3600))

func transcription(id int64) Transcription {
	return Transcription{
		ID: id, Text: "Hello, world.", RawText: "hello world", Model: "parakeet-tdt-0.6b-v2",
		Language: "en-US", DurationMS: 2500, StartedAt: started, Source: SourceDictation,
	}
}

// eventually waits up to five seconds for cond.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// stillAfter checks that cond holds for a while, for things that must not
// happen.
func stillAfter(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(300 * time.Millisecond); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if !cond() {
			t.Fatalf("%s no longer holds", what)
		}
	}
}

func TestSendsTheTranscription(t *testing.T) {
	t.Parallel()
	r := newReceiver(t)
	var f failures
	q := startQueue(t, testOptions(&f))
	id, err := q.Enqueue(ctx, Endpoint{URL: r.URL + "/transcriptions?token=x"}, transcription(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uuid.Parse(id); err != nil {
		t.Errorf("the delivery's ID %q is no UUID: %v", id, err)
	}
	eventually(t, "the POST", func() bool { return len(r.requests()) == 1 })
	got := r.requests()[0]
	if got.method != http.MethodPost || got.path != "/transcriptions" || got.contentType != "application/json" ||
		got.webhookID != id || got.userAgent != "Goscriber/test" {
		t.Errorf("request %+v", got)
	}
	var body map[string]any
	if err := json.Unmarshal(got.body, &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"transcription_id": 1.0,
		"text":             "Hello, world.",
		"raw_text":         "hello world",
		"model":            "parakeet-tdt-0.6b-v2",
		"language":         "en-US",
		"duration_ms":      2500.0,
		"started_at":       "2026-11-20T08:30:15.123Z",
		"source":           "dictation",
	}
	if len(body) != len(want) {
		t.Errorf("body %v, want the fields %v", body, want)
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("%s = %v, want %v", k, body[k], v)
		}
	}
	stillAfter(t, "one request and no failure", func() bool { return len(r.requests()) == 1 && len(f.list()) == 0 })
}

// A server that fails and then accepts gets every attempt with the same
// delivery ID and transcription ID.
func TestRetriesWithTheSameID(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusInternalServerError, http.StatusRequestTimeout, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()
			r := newReceiver(t, status, status, http.StatusNoContent)
			var f failures
			q := startQueue(t, testOptions(&f))
			id, err := q.Enqueue(ctx, Endpoint{URL: r.URL}, transcription(2))
			if err != nil {
				t.Fatal(err)
			}
			eventually(t, "three attempts", func() bool { return len(r.requests()) == 3 })
			for i, got := range r.requests() {
				var body Transcription
				if err := json.Unmarshal(got.body, &body); err != nil {
					t.Fatal(err)
				}
				if got.webhookID != id || body.ID != 2 || body.Text != "Hello, world." {
					t.Errorf("attempt %d: key %q, body %+v", i+1, got.webhookID, body)
				}
			}
			stillAfter(t, "no fourth attempt and no failure", func() bool { return len(r.requests()) == 3 && len(f.list()) == 0 })
		})
	}
}

// A client error other than 408 and 429, or a redirect that would turn the
// POST into a GET, is not retried, and is reported once.
func TestRefusalsAreNotRetried(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusNotFound, http.StatusGone, http.StatusUnprocessableEntity, http.StatusFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()
			r := newReceiver(t, status)
			var f failures
			q := startQueue(t, testOptions(&f))
			if _, err := q.Enqueue(ctx, Endpoint{URL: r.URL}, transcription(3)); err != nil {
				t.Fatal(err)
			}
			eventually(t, "the failure", func() bool { return len(f.list()) == 1 })
			stillAfter(t, "a single attempt and failure", func() bool { return len(r.requests()) == 1 && len(f.list()) == 1 })
			got := f.list()[0]
			wantErr := fmt.Sprintf("the server answered %d %s", status, http.StatusText(status))
			if got.ID != 3 || got.Host != strings.TrimPrefix(r.URL, "http://") || got.Attempts != 1 ||
				!strings.Contains(got.Err.Error(), wantErr) || !strings.HasSuffix(got.Err.Error(), ": nope") {
				t.Errorf("failure %+v, want %q and the body's first line", got, wantErr)
			}
			if pending, err := q.Pending(ctx); err != nil || pending {
				t.Errorf("pending %v, %v after a refusal", pending, err)
			}
		})
	}
}

// A 307 sends the POST on, body and all.
func TestFollowsRedirectsThatKeepThePOST(t *testing.T) {
	t.Parallel()
	target := newReceiver(t)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, target.URL+"/moved", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirect.Close)
	var f failures
	q := startQueue(t, testOptions(&f))
	if _, err := q.Enqueue(ctx, Endpoint{URL: redirect.URL}, transcription(4)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the POST at the target", func() bool { return len(target.requests()) == 1 })
	got := target.requests()[0]
	if got.method != http.MethodPost || got.path != "/moved" || !strings.Contains(string(got.body), `"transcription_id":4`) {
		t.Errorf("request %+v", got)
	}
}

// A server that never accepts gets every attempt, and the failure is
// reported once, after the last.
func TestGivesUpAfterTheLastAttempt(t *testing.T) {
	t.Parallel()
	r := newReceiver(t, http.StatusBadGateway)
	var f failures
	q := startQueue(t, testOptions(&f))
	if _, err := q.Enqueue(ctx, Endpoint{URL: r.URL}, transcription(5)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the failure", func() bool { return len(f.list()) == 1 })
	stillAfter(t, "three attempts and one failure", func() bool { return len(r.requests()) == 3 && len(f.list()) == 1 })
	if got := f.list()[0]; got.Attempts != 3 || !strings.Contains(got.Err.Error(), "502 Bad Gateway") {
		t.Errorf("failure %+v", got)
	}
}

// Network errors and timeouts are retried, and their messages do not show
// the URL, which may hold a token.
func TestNetworkErrorsAreRetriedWithoutShowingTheURL(t *testing.T) {
	t.Parallel()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close() // nothing listens there now

	var f failures
	q := startQueue(t, testOptions(&f))
	if _, err := q.Enqueue(ctx, Endpoint{URL: "http://" + addr + "/hook/s3cret?token=t0ken"}, transcription(6)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the failure", func() bool { return len(f.list()) == 1 })
	got := f.list()[0]
	if got.Attempts != 3 || got.Host != addr || strings.Contains(got.Err.Error(), "s3cret") || strings.Contains(got.Err.Error(), "t0ken") {
		t.Errorf("failure %+v", got)
	}

	slow := newReceiver(t)
	slow.hold = make(chan struct{}) // never answers
	opts := testOptions(&f)
	opts.attemptTimeout = 100 * time.Millisecond
	opts.maxAttempts = 2
	q = startQueue(t, opts)
	if _, err := q.Enqueue(ctx, Endpoint{URL: slow.URL}, transcription(7)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the timeout failure", func() bool { return len(f.list()) == 2 })
	if got := f.list()[1]; got.Attempts != 2 || got.Err.Error() != "no answer within 100ms" || len(slow.requests()) != 2 {
		t.Errorf("failure %+v after %d requests", got, len(slow.requests()))
	}
}

// A transcription queued before the queue sends is sent by the next one to
// open the database.
func TestQueuedTranscriptionSurvivesAClose(t *testing.T) {
	t.Parallel()
	r := newReceiver(t)
	var f failures
	path := filepath.Join(t.TempDir(), "jobs.sqlite3")

	q, err := Open(ctx, path, testOptions(&f))
	if err != nil {
		t.Fatal(err)
	}
	id, err := q.Enqueue(ctx, Endpoint{URL: r.URL}, transcription(8))
	if err != nil {
		t.Fatal(err)
	}
	if pending, err := q.Pending(ctx); err != nil || !pending {
		t.Fatalf("pending %v, %v after queueing", pending, err)
	}
	if err := q.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(r.requests()); n != 0 {
		t.Fatalf("%d requests before the queue started", n)
	}

	again := openQueue(t, path, testOptions(&f))
	if err := again.Start(ctx); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the POST from the new queue", func() bool { return len(r.requests()) == 1 })
	if got := r.requests()[0]; got.webhookID != id {
		t.Errorf("request %+v", got)
	}
	eventually(t, "nothing pending", func() bool {
		pending, err := again.Pending(ctx)
		return err == nil && !pending
	})
}

// An attempt cut short by Close is made again by the next queue, and does
// not count: with one attempt allowed, the transcription still arrives.
func TestInterruptedAttemptDoesNotCount(t *testing.T) {
	t.Parallel()
	r := newReceiver(t)
	r.hold = make(chan struct{})
	var f failures
	opts := testOptions(&f)
	opts.maxAttempts = 1
	path := filepath.Join(t.TempDir(), "jobs.sqlite3")

	q, err := Open(ctx, path, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	id, err := q.Enqueue(ctx, Endpoint{URL: r.URL}, transcription(9))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("the first attempt never arrived")
	}
	if err := q.Close(ctx); err != nil {
		t.Fatal(err)
	}

	r.mu.Lock()
	r.hold = nil
	r.mu.Unlock()
	again := openQueue(t, path, opts)
	if err := again.Start(ctx); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the second request", func() bool { return len(r.requests()) == 2 })
	for i, got := range r.requests() {
		if got.webhookID != id {
			t.Errorf("request %d: webhook-id %q, want %q from before the close", i+1, got.webhookID, id)
		}
	}
	eventually(t, "nothing pending", func() bool {
		pending, err := again.Pending(ctx)
		return err == nil && !pending
	})
	if got := f.list(); len(got) != 0 {
		t.Errorf("failures %+v", got)
	}
}

// Each delivery has its own ID, also of the same transcription: queueing it
// again is a new delivery, which the receiver must not drop as a repeat.
func TestEachDeliveryHasItsOwnID(t *testing.T) {
	t.Parallel()
	r := newReceiver(t)
	var f failures
	q := startQueue(t, testOptions(&f))
	first, err := q.Enqueue(ctx, Endpoint{URL: r.URL}, transcription(10))
	if err != nil {
		t.Fatal(err)
	}
	second, err := q.Enqueue(ctx, Endpoint{URL: r.URL}, transcription(10))
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("two deliveries share the ID %q", first)
	}
	eventually(t, "both POSTs", func() bool { return len(r.requests()) == 2 })
	ids := map[string]bool{}
	for _, got := range r.requests() {
		ids[got.webhookID] = true
	}
	if !ids[first] || !ids[second] {
		t.Errorf("webhook-ids %v, want %q and %q", ids, first, second)
	}
}

func TestEnqueueRefusesWhatCannotBeSent(t *testing.T) {
	t.Parallel()
	var f failures
	q := openQueue(t, filepath.Join(t.TempDir(), "jobs.sqlite3"), testOptions(&f))
	for _, tt := range []struct {
		e Endpoint
		t Transcription
	}{
		{Endpoint{}, transcription(1)},
		{Endpoint{URL: "ftp://example.com/x"}, transcription(1)},
		{Endpoint{URL: "example.com/hook"}, transcription(1)},
		{Endpoint{URL: "https://user:pass@example.com/hook"}, transcription(1)},
		{Endpoint{URL: "https://example.com/hook"}, transcription(0)},
		{Endpoint{URL: "https://example.com/hook", Auth: "digest"}, transcription(1)},
		{Endpoint{URL: "https://example.com/hook", Auth: config.WebhookAuthBasic, Username: "a:b"}, transcription(1)},
	} {
		if _, err := q.Enqueue(ctx, tt.e, tt.t); err == nil {
			t.Errorf("Enqueue(%+v, ID %d) succeeded", tt.e, tt.t.ID)
		}
	}
	if pending, err := q.Pending(ctx); err != nil || pending {
		t.Errorf("pending %v, %v", pending, err)
	}
}

func TestReadStart(t *testing.T) {
	long := strings.Repeat("é", 150) // 300 bytes
	for _, tt := range []struct{ in, want string }{
		{"", ""},
		{"  not found \n details", "not found"},
		{long, strings.Repeat("é", 100) + "…"},
	} {
		if got := readStart(strings.NewReader(tt.in)); got != tt.want {
			t.Errorf("readStart(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestErrorsForRefusals(t *testing.T) {
	if !errors.Is(&statusError{status: "404 Not Found", refused: true}, errRefused) {
		t.Error("a refused status is not errRefused")
	}
	if errors.Is(&statusError{status: "503 Service Unavailable"}, errRefused) {
		t.Error("a 503 counts as refused")
	}
}

// keychain is a keychain in memory, for Options.Secret; it counts the reads.
type keychain struct {
	mu      sync.Mutex
	secrets map[string]string
	err     error
	reads   int
}

func (k *keychain) get(account string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.reads++
	return k.secrets[account], k.err
}

func (k *keychain) set(account, secret string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.secrets == nil {
		k.secrets = map[string]string{}
	}
	k.secrets[account] = secret
}

// Basic and Bearer send the secret stored for the URL; none sends nothing.
func TestAuthentication(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		auth     config.WebhookAuth
		username string
		want     string
	}{
		{config.WebhookAuthNone, "", ""},
		{"", "", ""},
		{config.WebhookAuthBasic, "goscriber", "Basic Z29zY3JpYmVyOnMzY3JldA=="}, // goscriber:s3cret
		{config.WebhookAuthBasic, "", "Basic OnMzY3JldA=="},                      // :s3cret
		{config.WebhookAuthBearer, "ignored", "Bearer s3cret"},
	} {
		t.Run(string(tt.auth)+"/"+tt.username, func(t *testing.T) {
			t.Parallel()
			r := newReceiver(t)
			var f failures
			var k keychain
			k.set(r.URL, "s3cret")
			k.set(r.URL+"/other", "not this one")
			opts := testOptions(&f)
			opts.Secret = k.get
			q := startQueue(t, opts)
			if _, err := q.Enqueue(ctx, Endpoint{URL: r.URL, Auth: tt.auth, Username: tt.username}, transcription(11)); err != nil {
				t.Fatal(err)
			}
			eventually(t, "the POST", func() bool { return len(r.requests()) == 1 })
			if got := r.requests()[0].authorization; got != tt.want {
				t.Errorf("Authorization %q, want %q", got, tt.want)
			}
		})
	}
}

// Every attempt reads the secret again, so one replaced between attempts
// is the one sent; and one missing is retried until it is stored, without
// reaching the server meanwhile.
func TestSecretIsReadAtEveryAttempt(t *testing.T) {
	t.Parallel()
	r := newReceiver(t, http.StatusServiceUnavailable, http.StatusOK)
	var f failures
	var k keychain
	opts := testOptions(&f)
	opts.Secret = k.get
	opts.maxAttempts = 50
	opts.retryDelay = 20 * time.Millisecond
	q := startQueue(t, opts)
	if _, err := q.Enqueue(ctx, Endpoint{URL: r.URL, Auth: config.WebhookAuthBearer}, transcription(12)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "a few attempts without a token", func() bool {
		k.mu.Lock()
		defer k.mu.Unlock()
		return k.reads >= 2
	})
	if n := len(r.requests()); n != 0 {
		t.Fatalf("%d requests went out without the token", n)
	}
	k.set(r.URL, "first")
	eventually(t, "the attempt the server fails", func() bool { return len(r.requests()) == 1 })
	k.set(r.URL, "second")
	eventually(t, "the attempt it accepts", func() bool { return len(r.requests()) == 2 })
	if got := []string{r.requests()[0].authorization, r.requests()[1].authorization}; got[0] != "Bearer first" || got[1] != "Bearer second" {
		t.Errorf("Authorization %q, want the token as it was at each attempt", got)
	}
	if got := f.list(); len(got) != 0 {
		t.Errorf("failures %+v", got)
	}
}

// A secret never stored, or a keychain that cannot be read, fails the
// attempts and says so; no request goes out.
func TestMissingSecretIsReported(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		e    func(url string) Endpoint
		k    *keychain
		want string
	}{
		{
			"no password", func(u string) Endpoint { return Endpoint{URL: u, Auth: config.WebhookAuthBasic, Username: "me"} },
			&keychain{}, "no password for it is in the keychain",
		},
		{
			"no token", func(u string) Endpoint { return Endpoint{URL: u, Auth: config.WebhookAuthBearer} },
			&keychain{}, "no token for it is in the keychain",
		},
		{
			"locked keychain", func(u string) Endpoint { return Endpoint{URL: u, Auth: config.WebhookAuthBearer} },
			&keychain{err: errors.New("the keychain is locked")}, "its token could not be read from the keychain: the keychain is locked",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := newReceiver(t)
			var f failures
			opts := testOptions(&f)
			opts.Secret = tt.k.get
			q := startQueue(t, opts)
			if _, err := q.Enqueue(ctx, tt.e(r.URL), transcription(13)); err != nil {
				t.Fatal(err)
			}
			eventually(t, "the failure", func() bool { return len(f.list()) == 1 })
			if got := f.list()[0]; got.Attempts != 3 || got.Err.Error() != tt.want {
				t.Errorf("failure %+v, want 3 attempts and %q", got, tt.want)
			}
			if n := len(r.requests()); n != 0 {
				t.Errorf("%d requests went out", n)
			}
		})
	}
}

// A redirect to another host does not take the credentials along.
func TestCredentialsStayWithTheirHost(t *testing.T) {
	t.Parallel()
	target := newReceiver(t)
	// The target as localhost, a host other than the 127.0.0.1 it is
	// served on, which the redirect comes from.
	elsewhere := strings.Replace(target.URL, "127.0.0.1", "localhost", 1)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer s3cret" {
			http.Error(w, "no token", http.StatusUnauthorized)
			return
		}
		http.Redirect(w, req, elsewhere+"/moved", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirect.Close)
	var f failures
	var k keychain
	k.set(redirect.URL, "s3cret")
	opts := testOptions(&f)
	opts.Secret = k.get
	q := startQueue(t, opts)
	if _, err := q.Enqueue(ctx, Endpoint{URL: redirect.URL, Auth: config.WebhookAuthBearer}, transcription(14)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the POST at the other host", func() bool { return len(target.requests()) == 1 })
	if got := target.requests()[0]; got.authorization != "" || got.path != "/moved" {
		t.Errorf("request %+v: the token followed the redirect", got)
	}
}
