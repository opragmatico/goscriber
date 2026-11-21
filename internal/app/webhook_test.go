package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/notice"
	"github.com/opragmatico/goscriber/internal/ui"
	"github.com/opragmatico/goscriber/internal/webhook"
)

// hookPost is a POST a hookReceiver got.
type hookPost struct {
	webhook.Transcription
	userAgent, webhookID, authorization string
}

// hookReceiver is a webhook that accepts every POST and hands it on.
type hookReceiver struct {
	*httptest.Server
	posts chan hookPost
}

func newHookReceiver(t *testing.T) *hookReceiver {
	t.Helper()
	r := &hookReceiver{posts: make(chan hookPost, 10)}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		p := hookPost{userAgent: req.UserAgent(), webhookID: req.Header.Get("webhook-id"), authorization: req.Header.Get("Authorization")}
		if err := json.Unmarshal(body, &p.Transcription); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		r.posts <- p
	}))
	t.Cleanup(r.Close)
	return r
}

// next waits for the next POST.
func (r *hookReceiver) next(t *testing.T) hookPost {
	t.Helper()
	select {
	case p := <-r.posts:
		return p
	case <-time.After(10 * time.Second):
		t.Fatal("the webhook got nothing")
		return hookPost{}
	}
}

// none checks that no POST comes for a while.
func (r *hookReceiver) none(t *testing.T) {
	t.Helper()
	select {
	case p := <-r.posts:
		t.Errorf("the webhook got %+v", p)
	case <-time.After(1500 * time.Millisecond):
	}
}

// Dictations and files are sent to the webhook, each with its history
// entry's ID; while the history keeps nothing, each still gets an ID of its
// own, and the History page shows none of them.
func TestTranscriptionsAreSentToTheWebhook(t *testing.T) {
	home := isolate(t)
	testShortcuts.reset()
	hook := newHookReceiver(t)
	writeSettings(t, home, fmt.Sprintf(`{"schema_version": 1, "selected_model": "m", "webhook_url": %q}`, hook.URL+"/in"))
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	sh, q := newTestShell(t, a, Options{Version: "1.2.3"})
	out := sh.svc.output.(*testOutput)

	item(t, a.menu, ui.LabelStartDictation).Action()
	q.waitUI(t, "the recording icon", func() bool { return a.trayIcon == ui.TrayIcon(ui.TrayRecording) })
	item(t, a.menu, ui.LabelStopDictation).Action()
	q.waitUI(t, "the transcript", func() bool { return len(out.delivered()) == 1 })
	got := hook.next(t)
	entries, err := sh.history.List(context.Background(), 0)
	if err != nil || len(entries) != 1 {
		t.Fatalf("history %+v, %v", entries, err)
	}
	if got.ID != entries[0].ID || got.webhookID == "" || got.Source != webhook.SourceDictation ||
		got.Text != "hello from the test" || got.Model != "m" || got.DurationMS <= 0 || got.userAgent != "Goscriber/1.2.3" {
		t.Errorf("POST %+v for the entry %+v", got, entries[0])
	}

	path := writeTone(t, "interview.wav", 2*time.Second)
	e, err := sh.transcribeFile(context.Background(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if file := hook.next(t); file.ID != e.ID || file.Source != "interview.wav" || file.DurationMS != 2000 || file.Text != e.Text ||
		file.webhookID == "" || file.webhookID == got.webhookID {
		t.Errorf("POST %+v for the file's entry %+v, after the dictation's webhook-id %q", file, e, got.webhookID)
	}

	if err := sh.rt.Settings.Update(func(s *config.Settings) { s.HistoryLimit = 0 }); err != nil {
		t.Fatal(err)
	}
	q.waitUI(t, "the new retention", func() bool { return sh.history.Retention().Limit == 0 })
	unkept, err := sh.transcribeFile(context.Background(), path, nil)
	if err != nil || unkept.ID != 0 {
		t.Fatalf("with no history kept: %+v, %v", unkept, err)
	}
	if got := hook.next(t); got.ID <= e.ID || got.Source != "interview.wav" {
		t.Errorf("POST %+v with no history kept, want a new ID after %d", got, e.ID)
	}
	if n, err := sh.history.Count(context.Background()); err != nil || n != 0 {
		t.Errorf("%d entries shown with no history kept, %v", n, err)
	}
}

// Without a URL nothing is sent and no jobs database is made, and while the
// history keeps nothing no hidden entry is added either: no ID is used up.
// Setting a URL makes the database.
func TestNoWebhookWithoutAURL(t *testing.T) {
	home := isolate(t)
	testShortcuts.reset()
	writeSettings(t, home, `{"schema_version": 1, "selected_model": "m"}`)
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	sh, q := newTestShell(t, a, Options{})
	memo := writeTone(t, "memo.wav", time.Second)
	setLimit := func(n int) {
		t.Helper()
		if err := sh.rt.Settings.Update(func(s *config.Settings) { s.HistoryLimit = n }); err != nil {
			t.Fatal(err)
		}
		q.waitUI(t, "the new retention", func() bool { return sh.history.Retention().Limit == n })
	}

	first, err := sh.transcribeFile(context.Background(), memo, nil)
	if err != nil || first.ID == 0 {
		t.Fatalf("first file: %+v, %v", first, err)
	}
	setLimit(0)
	if e, err := sh.transcribeFile(context.Background(), memo, nil); err != nil || e.ID != 0 {
		t.Fatalf("with no history kept: %+v, %v", e, err)
	}
	setLimit(5)
	next, err := sh.transcribeFile(context.Background(), memo, nil)
	if err != nil || next.ID != first.ID+1 {
		t.Errorf("the next entry %+v, %v: want ID %d, none used up without a webhook", next, err, first.ID+1)
	}
	jobs := sh.rt.Paths.JobsDB()
	if _, err := os.Stat(jobs); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a jobs database without a URL: %v", err)
	}

	hook := newHookReceiver(t)
	if err := sh.rt.Settings.Update(func(s *config.Settings) { s.WebhookURL = hook.URL }); err != nil {
		t.Fatal(err)
	}
	q.waitUI(t, "the jobs database", func() bool {
		_, err := os.Stat(jobs)
		return err == nil
	})
	hook.none(t) // what was transcribed before is not sent
}

// Transcriptions an earlier run queued are sent at launch, also once the
// URL has been cleared, to the URL they were queued for.
func TestLeftoverTranscriptionsAreSentAtLaunch(t *testing.T) {
	home := isolate(t)
	hook := newHookReceiver(t)
	q, err := webhook.Open(context.Background(), filepath.Join(home, "jobs.sqlite3"), webhook.Options{})
	if err != nil {
		t.Fatal(err)
	}
	left := webhook.Transcription{ID: 7, Text: "Left over.", Model: "m", StartedAt: time.Now(), Source: webhook.SourceDictation}
	if _, err := q.Enqueue(context.Background(), webhook.Endpoint{URL: hook.URL}, left); err != nil {
		t.Fatal(err)
	}
	if err := q.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	writeSettings(t, home, `{"schema_version": 1, "selected_model": "m"}`)
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	newTestShell(t, a, Options{})
	if got := hook.next(t); got.ID != 7 || got.Text != "Left over." {
		t.Errorf("POST %+v, want the left-over transcription", got)
	}
}

// The webhook's password or token comes from the keychain, under its URL,
// and never reaches the settings. A transcription queued before it is
// stored raises a notice at once and waits for it.
func TestWebhookAuthentication(t *testing.T) {
	home := isolate(t)
	hook := newHookReceiver(t)
	writeSettings(t, home, fmt.Sprintf(`{"schema_version": 1, "selected_model": "m", "webhook_url": %q, "webhook_auth": "bearer"}`, hook.URL))
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	sh, q := newTestShell(t, a, Options{})
	keys := sh.webhookActions()
	if err := keys.SetKey(hook.URL, "t0ken"); err != nil {
		t.Fatal(err)
	}
	memo := writeTone(t, "memo.wav", time.Second)
	if _, err := sh.transcribeFile(context.Background(), memo, nil); err != nil {
		t.Fatal(err)
	}
	if got := hook.next(t); got.authorization != "Bearer t0ken" {
		t.Errorf("Authorization %q, want the token from the keychain", got.authorization)
	}
	if data, err := os.ReadFile(sh.rt.Paths.SettingsFile()); err != nil || strings.Contains(string(data), "t0ken") {
		t.Errorf("the token is in the settings file (%v)", err)
	}

	// Basic, with no password stored yet.
	if err := sh.rt.Settings.Update(func(s *config.Settings) {
		s.WebhookAuth, s.WebhookUsername = config.WebhookAuthBasic, "goscriber"
	}); err != nil {
		t.Fatal(err)
	}
	q.waitUI(t, "Basic in the window", func() bool { return sh.settings.Settings().WebhookAuth == config.WebhookAuthBasic })
	if err := keys.SetKey(hook.URL, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := sh.transcribeFile(context.Background(), memo, nil); err != nil {
		t.Fatal(err)
	}
	if n := sh.notices.Count(notice.CodeWebhookFailed); n != 1 {
		t.Errorf("%d webhook notices, want one saying the password is missing", n)
	}
	if active := sh.notices.Active(); len(active) == 0 || !strings.Contains(active[len(active)-1].Message, "no password for it is in the keychain") {
		t.Errorf("notices %+v", active)
	}
	hook.none(t)
	if err := keys.SetKey(hook.URL, "s3cret"); err != nil {
		t.Fatal(err)
	}
	if got := hook.next(t); got.authorization != "Basic Z29zY3JpYmVyOnMzY3JldA==" { // goscriber:s3cret
		t.Errorf("Authorization %q, want Basic with the stored password", got.authorization)
	}
}
