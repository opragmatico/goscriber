package models

import (
	"os"
	"path/filepath"
	"strings"
)

// HFCacheDir returns the Hugging Face hub cache directory, resolved as
// huggingface_hub resolves it: HF_HUB_CACHE, then the older
// HUGGINGFACE_HUB_CACHE, then $HF_HOME/hub, then
// $XDG_CACHE_HOME/huggingface/hub, then ~/.cache/huggingface/hub. getenv is
// os.Getenv outside tests. It returns "" when no home directory is known.
func HFCacheDir(getenv func(string) string) string {
	for _, key := range []string{"HF_HUB_CACHE", "HUGGINGFACE_HUB_CACHE"} {
		if v := getenv(key); v != "" {
			return expandHome(v, getenv)
		}
	}
	if v := getenv("HF_HOME"); v != "" {
		return filepath.Join(expandHome(v, getenv), "hub")
	}
	if v := getenv("XDG_CACHE_HOME"); v != "" {
		return filepath.Join(expandHome(v, getenv), "huggingface", "hub")
	}
	home := getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".cache", "huggingface", "hub")
}

// EndpointFromEnv returns HF_ENDPOINT, or DefaultEndpoint when it is unset.
func EndpointFromEnv(getenv func(string) string) string {
	if v := strings.TrimSuffix(getenv("HF_ENDPOINT"), "/"); v != "" {
		return v
	}
	return DefaultEndpoint
}

func expandHome(p string, getenv func(string) string) string {
	if rest, ok := strings.CutPrefix(p, "~"); ok && (rest == "" || rest[0] == '/' || rest[0] == filepath.Separator) {
		if home := getenv("HOME"); home != "" {
			return filepath.Join(home, rest)
		}
	}
	return p
}

// hfCandidates lists where the hub cache in dir may hold repo's file, best
// first: the pinned revision's snapshot, then the snapshot refs/main points
// to, then any other snapshot. Like Handy, Goscriber accepts a file from any
// snapshot, because the checksum, not the revision, decides whether it is
// the right one. Only paths that exist are returned. A snapshot entry that
// links to a blob named by a different SHA-256 is skipped without reading
// it: the hub names LFS blobs by their SHA-256.
func hfCandidates(dir, repo, revision, file, sha string) []string {
	if dir == "" {
		return nil
	}
	repoDir := filepath.Join(dir, "models--"+strings.ReplaceAll(repo, "/", "--"))
	snapshots := filepath.Join(repoDir, "snapshots")
	order := []string{revision}
	if main, err := os.ReadFile(filepath.Join(repoDir, "refs", "main")); err == nil {
		order = append(order, strings.TrimSpace(string(main)))
	}
	if entries, err := os.ReadDir(snapshots); err == nil {
		for _, e := range entries {
			order = append(order, e.Name())
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, rev := range order {
		if rev == "" || seen[rev] || strings.ContainsAny(rev, `/\`) || rev == "." || rev == ".." {
			continue
		}
		seen[rev] = true
		p := filepath.Join(snapshots, rev, file)
		if target, err := os.Readlink(p); err == nil {
			if blob := filepath.Base(target); isHex(blob, 64) && blob != sha {
				continue
			}
		}
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			out = append(out, p)
		}
	}
	return out
}
