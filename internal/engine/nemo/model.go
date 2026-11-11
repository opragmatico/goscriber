package nemo

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"

	"github.com/opragmatico/goscriber/internal/engine"
)

// Metadata keys NeMo-Speech.cpp's converter (conversion/asr.py) writes into
// ASR GGUFs. They are not part of the C ABI, so check them on every pin bump.
const (
	keyArchitecture     = "general.architecture"
	keyCacheSupported   = "asr.encoder.cache_supported"
	keyPromptDictionary = "asr.rnnt.prompt_dictionary"

	asrArchitecture = "asr"
)

// modelMeta is what the binding learns about a model from its GGUF metadata.
type modelMeta struct {
	// streaming: a cache-aware encoder, which can decode live. Offline-only
	// models such as Parakeet TDT cannot.
	streaming bool
	// languages: the locales of a prompt-conditioned multilingual model
	// (Nemotron 3.5), sorted, "auto" included; nil for single-language ones.
	languages []string
}

// inspectModel checks that path exists and is a NeMo-Speech.cpp ASR GGUF and
// reads what it can do. op names the operation in errors.
func inspectModel(op, path string) (modelMeta, error) {
	if err := checkFile(op, path); err != nil {
		return modelMeta{}, err
	}
	meta, err := readGGUFMetaFile(path, func(key string) bool {
		return key == keyArchitecture || key == keyCacheSupported || key == keyPromptDictionary
	})
	if err != nil {
		if errors.Is(err, errNotGGUF) {
			return modelMeta{}, &engine.Error{Op: op, Kind: engine.ErrInvalidArgument, Detail: path, Err: err}
		}
		return modelMeta{}, &engine.Error{Op: op, Kind: engine.ErrRuntime, Detail: path, Err: err}
	}
	if arch := meta.str(keyArchitecture); arch != asrArchitecture {
		return modelMeta{}, &engine.Error{
			Op: op, Kind: engine.ErrInvalidArgument,
			Detail: fmt.Sprintf("%s is not a NeMo-Speech.cpp ASR model (architecture %q)", path, arch),
		}
	}
	return modelMeta{
		streaming: meta.boolean(keyCacheSupported),
		languages: promptLanguages(meta.strings(keyPromptDictionary)),
	}, nil
}

// checkFile fails with engine.ErrModelNotFound when path does not exist.
func checkFile(op, path string) error {
	fi, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return &engine.Error{Op: op, Kind: engine.ErrModelNotFound, Detail: path}
	case err != nil:
		return &engine.Error{Op: op, Kind: engine.ErrRuntime, Err: err}
	case fi.IsDir():
		return &engine.Error{Op: op, Kind: engine.ErrInvalidArgument, Detail: path + " is a directory"}
	}
	return nil
}

// promptLanguages returns the languages in prompt dictionary entries, which
// map a language to a prompt index as "en-US:0".
func promptLanguages(entries []string) []string {
	var langs []string
	for _, e := range entries {
		if i := strings.LastIndexByte(e, ':'); i > 0 {
			langs = append(langs, e[:i])
		}
	}
	slices.Sort(langs)
	return slices.Compact(langs)
}
