package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"testing"
	"time"
)

func settingsPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "goscriber", "settings.json")
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func copyFixture(t *testing.T, name, path string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(data))
}

func TestMigrationsMatchSchemaVersion(t *testing.T) {
	if len(migrations) != SchemaVersion {
		t.Fatalf("%d migrations for schema version %d: add a migration with every version bump",
			len(migrations), SchemaVersion)
	}
}

func TestLoadMissingFileGivesDefaults(t *testing.T) {
	path := settingsPath(t)
	s, rep, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Created || !reflect.DeepEqual(s, Defaults()) {
		t.Errorf("got %+v / %+v, want defaults and Created", s, rep)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Error("Load must not write a settings file on its own")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := settingsPath(t)
	want := Defaults()
	want.SelectedModel = "parakeet-tdt-0.6b-v2"
	want.CustomWords = []string{"Goscriber", "Fyne"}
	want.CustomFillerWords = []string{}
	want.PasteMethod = PasteDirect
	want.AudioFeedbackVolume = 0.25
	want.ModelUnloadTimeout = UnloadNever

	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, rep, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip changed settings:\n got %+v\nwant %+v", got, want)
	}
	if rep.Created || rep.Migrated || rep.Reset || rep.Newer || len(rep.Fixed) > 0 {
		t.Errorf("unexpected report %+v", rep)
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("settings file mode %v, want 0600", info.Mode().Perm())
	}
	if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".settings-*.tmp")); len(leftovers) > 0 {
		t.Errorf("temporary files left behind: %v", leftovers)
	}
}

func TestLoadFillsMissingAndIgnoresUnknownFields(t *testing.T) {
	path := settingsPath(t)
	writeFile(t, path, `{"schema_version": 1, "history_limit": 9, "from_the_future": [1, 2]}`)

	got, rep, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := Defaults()
	want.HistoryLimit = 9
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want defaults with history_limit 9", got)
	}
	if len(rep.Fixed) > 0 {
		t.Errorf("unexpected fixes %v", rep.Fixed)
	}
}

func TestLoadMigratesV0Fixture(t *testing.T) {
	path := settingsPath(t)
	copyFixture(t, "v0.json", path)

	got, rep, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Migrated || rep.FromVersion != 0 {
		t.Errorf("report %+v, want a migration from v0", rep)
	}
	want := Defaults()
	want.TranscribeShortcut = "ctrl+shift+space"
	want.ActivationMode = ActivationPushToTalk
	want.SelectedModel = "nemotron-3.5-asr-streaming-0.6b"
	want.Language = "pt-BR"
	want.HistoryLimit = 20
	want.CustomWords = []string{"Goscriber", "NeMo"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("migrated settings:\n got %+v\nwant %+v", got, want)
	}
}

// v1 files hold filler_word_removal false, saved while it did nothing;
// v2 turns it on. From v2 on, false is the user's choice and stays.
func TestLoadMigratesV1FillerWordRemoval(t *testing.T) {
	path := settingsPath(t)
	writeFile(t, path, `{"schema_version": 1, "filler_word_removal": false, "history_limit": 7}`)
	got, rep, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Migrated || rep.FromVersion != 1 || !got.FillerWordRemoval || got.HistoryLimit != 7 || got.SchemaVersion != SchemaVersion {
		t.Errorf("v1: got %+v / %+v, want filler_word_removal on and the rest kept", got, rep)
	}

	writeFile(t, path, `{"schema_version": 2, "filler_word_removal": false}`)
	got, rep, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if rep.FromVersion != 2 || got.FillerWordRemoval {
		t.Errorf("v2: got %+v / %+v, want the user's false kept", got, rep)
	}
}

// v2 files have no llm_provider: v3 reads it from the base URL, and an
// address of no known provider is a custom server.
func TestLoadMigratesV2LLMProvider(t *testing.T) {
	for _, tc := range []struct {
		file string
		want LLMProvider
		url  string
	}{
		{`{"schema_version": 2}`, LLMOllama, DefaultLLMBaseURL},
		{`{"schema_version": 2, "llm_base_url": "http://localhost:11434/v1"}`, LLMOllama, DefaultLLMBaseURL},
		{`{"schema_version": 2, "llm_base_url": "https://api.groq.com/openai/v1/"}`, LLMGroq, "https://api.groq.com/openai/v1/"},
		{`{"schema_version": 2, "llm_base_url": "https://api.openai.com/v1"}`, LLMOpenAI, "https://api.openai.com/v1"},
		{`{"schema_version": 2, "llm_base_url": "http://localhost:1234/v1"}`, LLMCustom, "http://localhost:1234/v1"},
		{`{"schema_version": 2, "llm_base_url": 7}`, LLMOllama, DefaultLLMBaseURL},
	} {
		path := settingsPath(t)
		writeFile(t, path, tc.file)
		got, rep, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if !rep.Migrated || got.LLMProvider != tc.want || got.LLMBaseURL != tc.url {
			t.Errorf("%s: provider %q at %q (migrated %v), want %q at %q", tc.file, got.LLMProvider, got.LLMBaseURL, rep.Migrated, tc.want, tc.url)
		}
	}
}

// A provider the build does not know is the one at the base URL.
func TestLoadFixesUnknownLLMProvider(t *testing.T) {
	path := settingsPath(t)
	writeFile(t, path, `{"schema_version": 3, "llm_provider": "mistral", "llm_base_url": "https://openrouter.ai/api/v1"}`)
	got, rep, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.LLMProvider != LLMOpenRouter || !slices.Contains(rep.Fixed, "llm_provider") {
		t.Errorf("provider %q, fixed %v; want openrouter, fixed", got.LLMProvider, rep.Fixed)
	}
}

func TestLoadRunsMigrationsInOrder(t *testing.T) {
	path := settingsPath(t)
	writeFile(t, path, `{"schema_version": 1, "old_limit": 42}`)

	var ran []int
	migs := []migration{
		func(map[string]json.RawMessage) error { ran = append(ran, 0); return nil },
		func(doc map[string]json.RawMessage) error { // v1 -> v2: rename old_limit
			ran = append(ran, 1)
			doc["history_limit"] = doc["old_limit"]
			delete(doc, "old_limit")
			return nil
		},
		func(doc map[string]json.RawMessage) error { // v2 -> v3: change a value
			ran = append(ran, 2)
			doc["log_level"] = json.RawMessage(`"warn"`)
			return nil
		},
	}
	got, rep, err := load(path, migs)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ran, []int{1, 2}) {
		t.Errorf("ran migrations %v, want [1 2]", ran)
	}
	if got.SchemaVersion != 3 || got.HistoryLimit != 42 || got.LogLevel != LogWarn || !rep.Migrated {
		t.Errorf("got %+v / %+v", got, rep)
	}
}

func TestLoadMigrationErrorIsReturned(t *testing.T) {
	path := settingsPath(t)
	writeFile(t, path, `{}`)
	boom := errors.New("boom")
	_, _, err := load(path, []migration{func(map[string]json.RawMessage) error { return boom }})
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want it to wrap the migration error", err)
	}
}

func TestLoadCorruptFileIsMovedAside(t *testing.T) {
	now = func() time.Time { return time.Date(2026, 11, 11, 8, 30, 0, 0, time.UTC) }
	t.Cleanup(func() { now = time.Now })

	for name, content := range map[string]string{
		"truncated":      `{"history_limit": 3`,
		"not an object":  `[1, 2, 3]`,
		"null":           `null`,
		"bad version":    `{"schema_version": "one"}`,
		"negative":       `{"schema_version": -1}`,
		"empty":          ``,
		"random garbage": "\x00\x01settings",
	} {
		t.Run(name, func(t *testing.T) {
			path := settingsPath(t)
			writeFile(t, path, content)

			got, rep, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if !rep.Reset || !reflect.DeepEqual(got, Defaults()) {
				t.Errorf("got %+v / %+v, want defaults and Reset", got, rep)
			}
			if want := path + ".corrupt-20261111T083000Z"; rep.BackupPath != want {
				t.Errorf("BackupPath = %q, want %q", rep.BackupPath, want)
			}
			if data, err := os.ReadFile(rep.BackupPath); err != nil || string(data) != content {
				t.Errorf("backup does not hold the original file: %q, %v", data, err)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Error("the corrupt file should have been moved aside")
			}
		})
	}
}

func TestLoadResetsOnlyBadFields(t *testing.T) {
	path := settingsPath(t)
	writeFile(t, path, `{
		"schema_version": 1,
		"history_limit": "twenty",
		"paste_method": "telepathy",
		"custom_words": ["kept"],
		"language": "en-GB"
	}`)

	got, rep, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(rep.Fixed)
	if !slices.Equal(rep.Fixed, []string{"history_limit", "paste_method"}) {
		t.Errorf("Fixed = %v", rep.Fixed)
	}
	want := Defaults()
	want.CustomWords = []string{"kept"}
	want.Language = "en-GB"
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
}

func TestLoadNewerFileIsBackedUp(t *testing.T) {
	path := settingsPath(t)
	copyFixture(t, "newer.json", path)
	original, _ := os.ReadFile(path)

	got, rep, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Newer || rep.FromVersion != 99 || rep.Migrated {
		t.Errorf("report %+v", rep)
	}
	if got.HistoryLimit != 7 || got.SchemaVersion != SchemaVersion {
		t.Errorf("known fields should be used and the version set to ours: %+v", got)
	}
	if data, err := os.ReadFile(rep.BackupPath); err != nil || string(data) != string(original) {
		t.Errorf("backup %q does not hold the original: %v", rep.BackupPath, err)
	}
}

func TestLoadReadErrorIsReturned(t *testing.T) {
	path := settingsPath(t)
	if err := EnsureDir(path); err != nil { // a directory where the file should be
		t.Fatal(err)
	}
	if _, _, err := Load(path); err == nil {
		t.Error("expected an error when the settings path is a directory")
	}
}

func TestLoadModelUnloadTimeout(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value string
		want  UnloadTimeout
		fixed bool
	}{
		{"never", `"never"`, UnloadNever, false},
		{"seconds", `90`, 90, false},
		{"zero", `0`, 0, false},
		{"absent", ``, 30, false},
		{"over a day", `100000`, 30, true},
		{"negative", `-5`, 30, true},
		{"other word", `"5m"`, 30, true}, // an old build's choice: not seconds, so the default
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := settingsPath(t)
			doc := `{"schema_version": 1}`
			if tt.value != "" {
				doc = `{"schema_version": 1, "model_unload_timeout": ` + tt.value + `}`
			}
			writeFile(t, path, doc)
			got, rep, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if got.ModelUnloadTimeout != tt.want {
				t.Errorf("ModelUnloadTimeout = %d, want %d", got.ModelUnloadTimeout, tt.want)
			}
			if fixed := slices.Contains(rep.Fixed, "model_unload_timeout"); fixed != tt.fixed {
				t.Errorf("Fixed = %v, want model_unload_timeout reported: %v", rep.Fixed, tt.fixed)
			}
		})
	}
}

// A webhook_url that is no http or https URL is reset to none.
func TestLoadResetsABadWebhookURL(t *testing.T) {
	for _, tt := range []struct {
		in, want string
		fixed    bool
	}{
		{"https://hooks.example.com/goscriber?token=t", "https://hooks.example.com/goscriber?token=t", false},
		{"http://localhost:8080/in", "http://localhost:8080/in", false},
		{"", "", false},
		{"hooks.example.com/in", "", true},
		{"ftp://hooks.example.com/in", "", true},
		{"https://", "", true},
		{" https://hooks.example.com", "", true},
		{"https://me:s3cret@hooks.example.com/in", "", true},
	} {
		path := settingsPath(t)
		writeFile(t, path, fmt.Sprintf(`{"schema_version": 1, "webhook_url": %q}`, tt.in))
		got, rep, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if got.WebhookURL != tt.want || slices.Contains(rep.Fixed, "webhook_url") != tt.fixed {
			t.Errorf("%q: loaded %q, fixed %v", tt.in, got.WebhookURL, rep.Fixed)
		}
	}
}

// A webhook_auth Goscriber does not know, or a user name HTTP Basic cannot
// carry, is reset; the defaults send no credentials.
func TestLoadResetsBadWebhookAuth(t *testing.T) {
	if d := Defaults(); d.WebhookAuth != WebhookAuthNone || d.WebhookUsername != "" {
		t.Errorf("defaults %q, %q", d.WebhookAuth, d.WebhookUsername)
	}
	for _, tt := range []struct {
		auth, user         string
		wantAuth, wantUser string
		fixed              []string
	}{
		{"basic", "goscriber", "basic", "goscriber", nil},
		{"bearer", "", "bearer", "", nil},
		{"digest", "goscriber", "none", "goscriber", []string{"webhook_auth"}},
		{"basic", "go:scriber", "basic", "", []string{"webhook_username"}},
		{"basic", "go\nscriber", "basic", "", []string{"webhook_username"}},
	} {
		path := settingsPath(t)
		writeFile(t, path, fmt.Sprintf(`{"schema_version": 1, "webhook_auth": %q, "webhook_username": %q}`, tt.auth, tt.user))
		got, rep, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(rep.Fixed)
		if string(got.WebhookAuth) != tt.wantAuth || got.WebhookUsername != tt.wantUser || !slices.Equal(rep.Fixed, tt.fixed) {
			t.Errorf("%q/%q: loaded %q/%q, fixed %v", tt.auth, tt.user, got.WebhookAuth, got.WebhookUsername, rep.Fixed)
		}
	}
}
