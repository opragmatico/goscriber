package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolvePathsHomeOverride(t *testing.T) {
	want := filepath.Join(t.TempDir(), "portable")
	t.Setenv(HomeEnv, want)

	p, err := ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	if p.ConfigDir != want || p.CacheDir != want {
		t.Fatalf("got %+v, want both dirs = %s", p, want)
	}
	for name, got := range map[string]string{
		"settings":   p.SettingsFile(),
		"history":    p.HistoryDB(),
		"recordings": p.RecordingsDir(),
		"models":     p.ModelsDir(),
		"logs":       p.LogsDir(),
	} {
		if !strings.HasPrefix(got, want+string(filepath.Separator)) {
			t.Errorf("%s path %s is outside %s", name, got, want)
		}
	}
}

func TestResolvePathsRelativeHomeIsMadeAbsolute(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv(HomeEnv, "portable")

	p, err := ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(p.ConfigDir) || filepath.Base(p.ConfigDir) != "portable" {
		t.Errorf("ConfigDir = %s, want an absolute path ending in portable", p.ConfigDir)
	}
}

func TestResolvePathsDefaultsToUserDirs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("user dirs come from %AppData% / %LocalAppData% on Windows")
	}
	home := t.TempDir()
	t.Setenv(HomeEnv, "")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")

	p, err := ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{p.ConfigDir, p.CacheDir} {
		if !strings.HasPrefix(d, home) || filepath.Base(d) != "goscriber" {
			t.Errorf("dir %s should be a goscriber dir under %s", d, home)
		}
	}
	if p.ConfigDir == p.CacheDir {
		t.Errorf("config and cache dirs should differ, both are %s", p.ConfigDir)
	}
}

func TestEnsureDirIsPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	if err := EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Errorf("mode %v, want 0700", info.Mode().Perm())
	}
	if err := EnsureDir(dir); err != nil {
		t.Errorf("second call: %v", err)
	}
}
