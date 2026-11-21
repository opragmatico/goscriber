package webhook

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riversqlite"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/riverqueue/river/rivertype"
	_ "modernc.org/sqlite" // the "sqlite" database/sql driver, in pure Go

	"github.com/opragmatico/goscriber/internal/config"
)

// Transcription is what the webhook receives: the JSON body of the POST.
type Transcription struct {
	// ID identifies the transcription: its history entry's ID, for the
	// receiver to match a POST to it. It is no key to drop repeats by:
	// each machine numbers its own entries. The webhook-id header is.
	ID int64 `json:"transcription_id"`
	// Text is what was delivered, after the text stages.
	Text string `json:"text"`
	// RawText is the model's transcript, before the text stages.
	RawText string `json:"raw_text"`
	// Model is the catalog ID of the model that transcribed it.
	Model string `json:"model"`
	// Language is the transcript's language when known, such as "en-US",
	// else "".
	Language string `json:"language"`
	// DurationMS is how long the recording or the file lasts, in
	// milliseconds.
	DurationMS int64 `json:"duration_ms"`
	// StartedAt is when recording started, or for a file when its
	// transcription started. Enqueue keeps it in UTC to the millisecond.
	StartedAt time.Time `json:"started_at"`
	// Source is SourceDictation, or the name of the audio file it was
	// transcribed from.
	Source string `json:"source"`
}

// SourceDictation is the Source of a dictation.
const SourceDictation = "dictation"

const (
	// MaxAttempts is how many times a transcription is tried before it is
	// given up. River waits attempt⁴ seconds after each failure, so the
	// last of 14 attempts comes about 25 hours after the first.
	MaxAttempts = 14
	// AttemptTimeout bounds one attempt, the server's answer included.
	AttemptTimeout = 30 * time.Second
	// workers is how many transcriptions are sent at once.
	workers = 2
)

// Retention of finished jobs, whose arguments hold the transcription. One
// that arrived is deleted soon after; one given up is kept for a week, to
// look into.
const (
	sentRetention   = time.Minute
	failedRetention = 7 * 24 * time.Hour
)

// KeychainService is the name the OS keychain shows the webhooks'
// passwords and tokens under, each under its URL (KeyAccount).
const KeychainService = "Goscriber webhook secret"

// KeyAccount is the keychain account a webhook URL's password or token is
// stored under: the URL itself. Each URL keeps its own, so a job queued
// for one URL never sends the secret of another.
func KeyAccount(webhookURL string) string { return strings.TrimSpace(webhookURL) }

// Endpoint is where transcriptions go, and how the POSTs authenticate.
type Endpoint struct {
	URL string
	// Auth is how the POSTs authenticate. Basic and Bearer send the
	// password or token Options.Secret returns for KeyAccount(URL).
	Auth config.WebhookAuth
	// Username is Basic's user name.
	Username string
}

// EndpointFrom is the endpoint the settings name; its URL is "" when there
// is no webhook.
func EndpointFrom(s config.Settings) Endpoint {
	return Endpoint{URL: s.WebhookURL, Auth: s.WebhookAuth, Username: s.WebhookUsername}
}

// NeedsSecret reports whether the endpoint's authentication sends a
// password or token from the keychain.
func (e Endpoint) NeedsSecret() bool {
	return e.Auth == config.WebhookAuthBasic || e.Auth == config.WebhookAuthBearer
}

// SecretName is what the endpoint's secret is called: "password" or
// "token".
func (e Endpoint) SecretName() string {
	if e.Auth == config.WebhookAuthBasic {
		return "password"
	}
	return "token"
}

// args are a job's arguments, stored as JSON in the jobs database: the
// delivery's ID, the endpoint the transcription was queued for, without
// its password or token, and the transcription.
type args struct {
	// ID identifies the delivery, in the webhook-id header: a random UUID,
	// made when the job is queued and stored with it, so every attempt
	// carries the same one, and no other delivery, from this machine or
	// another, ever does.
	ID            string             `json:"id"`
	URL           string             `json:"url"`
	Auth          config.WebhookAuth `json:"auth,omitempty"`
	Username      string             `json:"username,omitempty"`
	Transcription Transcription      `json:"transcription"`
}

func (a args) endpoint() Endpoint { return Endpoint{URL: a.URL, Auth: a.Auth, Username: a.Username} }

// Kind implements river.JobArgs.
func (args) Kind() string { return "transcription_webhook" }

// Failure is a transcription that will not be sent.
type Failure struct {
	// ID is the transcription's.
	ID int64
	// Host is the webhook URL's host: the URL itself may hold a secret, so
	// messages name the host alone.
	Host string
	// Attempts is how many times it was tried.
	Attempts int
	// Err is why the last attempt failed.
	Err error
}

// Options configure a Queue. The zero value works.
type Options struct {
	// Client sends the requests. Nil uses one that follows only the
	// redirects that keep the method and body (307 and 308).
	Client *http.Client
	// UserAgent is the requests' User-Agent header; "" leaves Go's.
	UserAgent string
	// OnFailure hears once about each transcription that will not be sent:
	// the server refused it, or MaxAttempts attempts failed. It runs on the
	// job's goroutine.
	OnFailure func(Failure)
	// Log gets a line per failed attempt, and River's own warnings and
	// errors. Nil logs nothing.
	Log *slog.Logger
	// Secret returns the password or token stored for a keychain account
	// (KeyAccount), or "" when there is none. Every attempt that
	// authenticates asks, so a secret replaced meanwhile is the one sent.
	// Nil has none.
	Secret func(account string) (string, error)

	// Tests shorten these; zero means the default.
	maxAttempts    int
	attemptTimeout time.Duration
	pollInterval   time.Duration
	// retryDelay replaces River's backoff with a fixed delay.
	retryDelay time.Duration
}

// Queue sends transcriptions to webhooks. Open it, Start it to send, and
// Close it when done; Enqueue works before Start too.
type Queue struct {
	db     *sql.DB
	client *river.Client[*sql.Tx]
}

// Open opens the jobs database at path, creating it and its directory if
// need be and migrating River's schema, and returns a Queue that does not
// send until Start.
func Open(ctx context.Context, path string, opts Options) (*Queue, error) {
	if err := config.EnsureDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("webhook: open %s: %w", path, err)
	}
	// One connection, as River's SQLite driver advises: SQLite runs one
	// statement at a time, and River's services would otherwise get
	// SQLITE_BUSY from each other.
	db.SetMaxOpenConns(1)
	q, err := newQueue(ctx, db, opts)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("webhook: %s: %w", path, err)
	}
	return q, nil
}

func newQueue(ctx context.Context, db *sql.DB, opts Options) (*Queue, error) {
	log := opts.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	// River tells of every client start, leadership change and failed
	// attempt at the info level; the worker logs the attempts itself.
	riverLog := slog.New(atLeast{log.Handler(), slog.LevelWarn})

	driver := riversqlite.New(db)
	migrator, err := rivermigrate.New(driver, &rivermigrate.Config{Logger: riverLog})
	if err != nil {
		return nil, err
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return nil, fmt.Errorf("migrate the jobs schema: %w", err)
	}

	timeout := cmp.Or(opts.attemptTimeout, AttemptTimeout)
	poll := cmp.Or(opts.pollInterval, river.FetchPollIntervalDefault)
	ws := river.NewWorkers()
	river.AddWorker(ws, &worker{
		client:    cmp.Or(opts.Client, defaultClient),
		userAgent: opts.UserAgent,
		timeout:   timeout,
		onFailure: opts.OnFailure,
		secret:    opts.Secret,
		log:       log,
	})
	cfg := &river.Config{
		Queues:  map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: workers}},
		Workers: ws,
		// River's SQLite driver emulates LISTEN with a table it polls every
		// 50 ms. A desktop app has no use for that: jobs are fetched every
		// poll interval instead, a second by default.
		PollOnly:                    true,
		FetchPollInterval:           poll,
		FetchCooldown:               min(river.FetchCooldownDefault, poll),
		MaxAttempts:                 cmp.Or(opts.maxAttempts, MaxAttempts),
		JobTimeout:                  timeout,
		RescueStuckJobsAfter:        timeout + time.Minute,
		CompletedJobRetentionPeriod: sentRetention,
		CancelledJobRetentionPeriod: failedRetention,
		DiscardedJobRetentionPeriod: failedRetention,
		Logger:                      riverLog,
	}
	if opts.retryDelay > 0 {
		cfg.RetryPolicy = fixedRetry(opts.retryDelay)
	}
	client, err := river.NewClient(driver, cfg)
	if err != nil {
		return nil, err
	}
	return &Queue{db: db, client: client}, nil
}

// Start starts sending: the transcriptions already queued, then each one
// enqueued. Cancelling ctx stops sending, as Close does.
func (q *Queue) Start(ctx context.Context) error {
	return q.client.Start(ctx)
}

// Enqueue queues t to be sent to the endpoint and returns the delivery's
// ID, which every attempt sends as webhook-id. The job keeps the endpoint,
// without its secret, so a later change of the settings leaves it alone.
func (q *Queue) Enqueue(ctx context.Context, e Endpoint, t Transcription) (string, error) {
	if e.URL == "" {
		return "", errors.New("webhook: no URL")
	}
	if err := config.CheckWebhookURL(e.URL); err != nil {
		return "", fmt.Errorf("webhook: %w", err)
	}
	if err := config.CheckWebhookUsername(e.Username); err != nil {
		return "", fmt.Errorf("webhook: %w", err)
	}
	if t.ID <= 0 {
		return "", errors.New("webhook: the transcription has no ID")
	}
	t.StartedAt = t.StartedAt.UTC().Truncate(time.Millisecond)
	a := args{ID: uuid.NewString(), URL: e.URL, Transcription: t}
	switch e.Auth {
	case config.WebhookAuthNone, "":
	case config.WebhookAuthBasic:
		a.Auth, a.Username = e.Auth, e.Username
	case config.WebhookAuthBearer:
		a.Auth = e.Auth
	default:
		return "", fmt.Errorf("webhook: unknown authentication %q", e.Auth)
	}
	if _, err := q.client.Insert(ctx, a, nil); err != nil {
		return "", fmt.Errorf("webhook: queue the transcription: %w", err)
	}
	return a.ID, nil
}

// Pending reports whether any transcription is still waiting to be sent,
// or being sent.
func (q *Queue) Pending(ctx context.Context) (bool, error) {
	res, err := q.client.JobList(ctx, river.NewJobListParams().States(
		rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRetryable,
		rivertype.JobStateRunning, rivertype.JobStateScheduled,
	).First(1))
	if err != nil {
		return false, fmt.Errorf("webhook: list the jobs: %w", err)
	}
	return len(res.Jobs) > 0, nil
}

// Close stops sending and closes the database. Requests in flight are cut
// short and made again after the next Start, without counting as attempts.
// Close returns once they have ended, or when ctx does.
func (q *Queue) Close(ctx context.Context) error {
	err := q.client.StopAndCancel(ctx)
	return errors.Join(err, q.db.Close())
}

// fixedRetry retries a failed job after a fixed delay, for tests.
type fixedRetry time.Duration

func (d fixedRetry) NextRetry(*rivertype.JobRow) time.Time { return time.Now().Add(time.Duration(d)) }

// atLeast passes on the records of level min and above.
type atLeast struct {
	slog.Handler
	min slog.Level
}

func (h atLeast) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= h.min && h.Handler.Enabled(ctx, l)
}

func (h atLeast) WithAttrs(attrs []slog.Attr) slog.Handler {
	return atLeast{h.Handler.WithAttrs(attrs), h.min}
}

func (h atLeast) WithGroup(name string) slog.Handler {
	return atLeast{h.Handler.WithGroup(name), h.min}
}
