package app

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/notice"
	"github.com/opragmatico/goscriber/internal/ui"
	"github.com/opragmatico/goscriber/internal/webhook"
)

// webhookCloseTimeout bounds how long quitting waits for the requests in
// flight to end; the ones cut short are sent again at the next launch.
const webhookCloseTimeout = 3 * time.Second

// webhooks sends finished transcriptions to the webhook_url setting through
// a webhook.Queue. The queue opens on first need: at launch when a URL is
// set, or when transcriptions queued by an earlier run still wait, and when
// the user sets a URL. Without a URL no jobs database is created, and
// nothing polls one.
type webhooks struct {
	path    string
	opts    webhook.Options
	log     *slog.Logger
	notices *notice.Hub
	// keys keeps the webhooks' passwords and tokens, by webhook.KeyAccount.
	keys secrets

	mu     sync.Mutex
	queue  *webhook.Queue // nil until opened
	closed bool
}

func newWebhooks(rt *Runtime, notices *notice.Hub, keys secrets, version string) *webhooks {
	log := rt.Log.With("component", "webhook")
	agent := "Goscriber"
	if version != "" {
		agent += "/" + version
	}
	return &webhooks{
		path:    rt.Paths.JobsDB(),
		log:     log,
		notices: notices,
		keys:    keys,
		opts: webhook.Options{
			UserAgent: agent,
			Log:       log,
			OnFailure: func(f webhook.Failure) { notices.Publish(notice.WebhookFailed(f.Host, f.Attempts, f.Err)) },
			Secret:    keys.Get,
		},
	}
}

// launch opens the queue in the background when there is something to send:
// a URL is set, or a URL was set before and its jobs database still holds
// transcriptions waiting to be sent.
func (w *webhooks) launch(s config.Settings) {
	go func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if s.WebhookURL != "" {
			if _, err := w.queueLocked(); err != nil {
				w.unavailable(s.WebhookURL, err)
			}
			return
		}
		if _, err := os.Stat(w.path); errors.Is(err, fs.ErrNotExist) {
			return
		}
		if err := w.drainLocked(); err != nil {
			w.log.Warn("the transcriptions left from an earlier run could not be sent", "err", err)
		}
	}()
}

// drainLocked opens the queue if transcriptions are still waiting in it,
// and leaves it closed otherwise.
func (w *webhooks) drainLocked() error {
	if w.closed || w.queue != nil {
		return nil
	}
	ctx := context.Background()
	q, err := webhook.Open(ctx, w.path, w.opts)
	if err != nil {
		return err
	}
	pending, err := q.Pending(ctx)
	if err == nil && pending {
		if err = q.Start(ctx); err == nil {
			w.log.Info("sending the transcriptions left from an earlier run")
			w.queue = q
			return nil
		}
	}
	return errors.Join(err, q.Close(ctx))
}

// follow opens the queue when the user sets a URL, so a database that
// cannot be opened is reported then rather than at the next dictation.
func (w *webhooks) follow(s config.Settings) {
	if s.WebhookURL == "" {
		return
	}
	go func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if _, err := w.queueLocked(); err != nil {
			w.unavailable(s.WebhookURL, err)
		}
	}()
}

// send queues t for the endpoint; nothing without a URL, or without text.
// An endpoint whose password or token the keychain does not hold gets a
// notice at once: the transcription waits for it, and would otherwise
// only be reported once its attempts ran out, a day later.
func (w *webhooks) send(e webhook.Endpoint, t webhook.Transcription) {
	if e.URL == "" || strings.TrimSpace(t.Text) == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	q, err := w.queueLocked()
	if err != nil {
		w.unavailable(e.URL, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	id, err := q.Enqueue(ctx, e, t)
	if err != nil {
		w.log.Warn("the transcription could not be queued for the webhook", "transcription", t.ID, "err", err)
		w.notices.Publish(notice.WebhookUnavailable(hostOf(e.URL), err))
		return
	}
	w.log.Debug("transcription queued for the webhook", "transcription", t.ID, "webhook_id", id)
	if e.NeedsSecret() {
		if secret, err := w.keys.Get(webhook.KeyAccount(e.URL)); err != nil || secret == "" {
			w.notices.Publish(notice.WebhookSecretMissing(hostOf(e.URL), e.SecretName(), err))
		}
	}
}

// webhookActions back the webhook's password or token on the Advanced
// page: kept in the keychain under the URL, never in the settings.
func (sh *shell) webhookActions() ui.WebhookActions {
	keys := sh.svc.webhookKeys
	return ui.WebhookActions{
		HasKey: func(webhookURL string) (bool, error) {
			secret, err := keys.Get(webhook.KeyAccount(webhookURL))
			return secret != "", err
		},
		SetKey: func(webhookURL, secret string) error { return keys.Set(webhook.KeyAccount(webhookURL), secret) },
	}
}

// queueLocked returns the queue, opening and starting it if need be. w.mu
// must be held.
func (w *webhooks) queueLocked() (*webhook.Queue, error) {
	if w.closed {
		return nil, errors.New("the app is quitting")
	}
	if w.queue != nil {
		return w.queue, nil
	}
	ctx := context.Background()
	q, err := webhook.Open(ctx, w.path, w.opts)
	if err != nil {
		return nil, err
	}
	if err := q.Start(ctx); err != nil {
		return nil, errors.Join(err, q.Close(ctx))
	}
	w.queue = q
	return q, nil
}

// unavailable reports a queue that could not be opened.
func (w *webhooks) unavailable(webhookURL string, err error) {
	if w.closed {
		return
	}
	w.log.Warn("the webhook's jobs database could not be opened", "err", err)
	w.notices.Publish(notice.WebhookUnavailable(hostOf(webhookURL), err))
}

// close stops sending, giving the requests in flight a moment to end.
func (w *webhooks) close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	if w.queue == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), webhookCloseTimeout)
	defer cancel()
	if err := w.queue.Close(ctx); err != nil {
		w.log.Warn("closing the webhook queue failed", "err", err)
	}
	w.queue = nil
}

// hostOf is u's host, the part of a webhook URL safe to show.
func hostOf(u string) string {
	if p, err := url.Parse(u); err == nil && p.Host != "" {
		return p.Host
	}
	return "the webhook"
}
