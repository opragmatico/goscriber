package notice

import (
	"errors"
	"fmt"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/opragmatico/goscriber/hook"
	"github.com/opragmatico/goscriber/internal/engine"
	"github.com/opragmatico/goscriber/internal/output"
)

// Code names a kind of problem. The hub de-duplicates notices by code.
type Code string

// Codes.
const (
	CodeNoModel               Code = "no_model"
	CodeMicrophoneUnavailable Code = "microphone_unavailable"
	CodePermissionMissing     Code = "permission_missing"
	CodeMicrophoneDenied      Code = "microphone_denied"
	CodeModelCorrupt          Code = "model_corrupt"
	CodeEngineCrashed         Code = "engine_crashed"
	CodeEngineCPUFallback     Code = "engine_cpu_fallback"
	CodeTranscriptionFailed   Code = "transcription_failed"
	CodePasteFailed           Code = "paste_failed"
	CodeShortcutFailed        Code = "shortcut_failed"
	CodeLoginItemFailed       Code = "login_item_failed"
	CodeHookFailed            Code = "hook_failed"
)

// Level is how bad a problem is.
type Level int

// Levels.
const (
	// LevelWarning: the user got a result, but not the one they set up,
	// such as transcribing on the CPU after the GPU failed.
	LevelWarning Level = iota
	// LevelError: nothing came out, or Goscriber cannot do its job.
	LevelError
)

func (l Level) String() string {
	if l == LevelError {
		return "error"
	}
	return "warning"
}

// Source is the part of Goscriber a notice comes from.
type Source string

// Sources.
const (
	SourceModel       Source = "model"
	SourceAudio       Source = "audio"
	SourcePermissions Source = "permissions"
	SourceEngine      Source = "engine"
	SourceOutput      Source = "output"
	SourceShortcut    Source = "shortcut"
	SourceApp         Source = "app"
	SourceText        Source = "text"
)

// ActionID is what a notice's button does; the UI carries it out.
type ActionID string

// Actions.
const (
	// ActionOpenModels opens the Models page (onboarding, once it exists),
	// to download or replace a model.
	ActionOpenModels ActionID = "open_models"
	// ActionOpenAccessibility opens the Accessibility pane of System
	// Settings.
	ActionOpenAccessibility ActionID = "open_accessibility"
	// ActionOpenMicrophone opens the Microphone pane of System Settings.
	ActionOpenMicrophone ActionID = "open_microphone"
	// ActionOpenSettings opens the settings window.
	ActionOpenSettings ActionID = "open_settings"
	// ActionOpenLoginItems opens the Login Items pane of System Settings.
	ActionOpenLoginItems ActionID = "open_login_items"
)

// Action is a notice's button.
type Action struct {
	Label string
	ID    ActionID
}

// Notice is a problem to tell the user about.
type Notice struct {
	Code   Code
	Level  Level
	Source Source
	// Message says what happened and, when it helps, what to do, in a
	// sentence or two ready to show.
	Message string
	// Action is the button that fixes it, if any; a zero Action is none.
	Action Action
	// At is when it was first published; the hub sets it.
	At time.Time
}

// kinds holds what each code implies.
var kinds = map[Code]struct {
	title      string
	level      Level
	source     Source
	persistent bool
}{
	CodeNoModel:               {"No model downloaded", LevelError, SourceModel, true},
	CodeMicrophoneUnavailable: {"Microphone unavailable", LevelError, SourceAudio, false},
	CodePermissionMissing:     {"Permission needed", LevelError, SourcePermissions, true},
	CodeMicrophoneDenied:      {"Microphone access needed", LevelError, SourcePermissions, true},
	CodeModelCorrupt:          {"Model file damaged", LevelError, SourceModel, false},
	CodeEngineCrashed:         {"Speech engine crashed", LevelError, SourceEngine, false},
	CodeEngineCPUFallback:     {"Transcribing on the CPU", LevelWarning, SourceEngine, false},
	CodeTranscriptionFailed:   {"Transcription failed", LevelError, SourceEngine, false},
	CodePasteFailed:           {"Could not paste", LevelError, SourceOutput, false},
	CodeShortcutFailed:        {"Shortcut unavailable", LevelError, SourceShortcut, false},
	CodeLoginItemFailed:       {"Launch at login failed", LevelWarning, SourceApp, false},
	CodeHookFailed:            {"Transcription hook failed", LevelWarning, SourceText, false},
}

// Title is a short heading for the notice, for an OS notification or the
// tray menu.
func (n Notice) Title() string {
	if k, ok := kinds[n.Code]; ok {
		return k.title
	}
	return string(n.Code)
}

// Persistent reports whether the notice describes a state that lasts until
// it is fixed, such as a missing permission or model, rather than an event.
// Persistent notices stay active until resolved, and the tray shows them.
func (n Notice) Persistent() bool { return kinds[n.Code].persistent }

// New returns a notice with the level and source its code implies.
func New(code Code, message string) Notice {
	k := kinds[code]
	return Notice{Code: code, Level: k.level, Source: k.source, Message: message}
}

// NoModel is the notice for no speech model downloaded, so dictation
// cannot start.
func NoModel() Notice {
	n := New(CodeNoModel, "Download a speech model to start dictating. Everything runs on this computer.")
	n.Action = Action{Label: "Download a model", ID: ActionOpenModels}
	return n
}

// MicrophoneUnavailable is the notice for a microphone that could not be
// opened, or went away; message is the recorder's.
func MicrophoneUnavailable(message string) Notice {
	return New(CodeMicrophoneUnavailable, message)
}

// AccessibilityMissing is the notice for the macOS Accessibility permission
// missing, which the shortcut and pasting need.
func AccessibilityMissing() Notice {
	n := New(CodePermissionMissing, "Goscriber needs the Accessibility permission to hear the shortcut and paste. "+
		"Turn it on for Goscriber in System Settings → Privacy & Security → Accessibility.")
	n.Action = Action{Label: "Open System Settings", ID: ActionOpenAccessibility}
	return n
}

// MicrophoneDenied is the notice for the macOS microphone permission
// refused, or restricted by a device policy, so recordings would be silent.
func MicrophoneDenied() Notice {
	n := New(CodeMicrophoneDenied, "Goscriber needs the microphone to hear you. "+
		"Turn it on for Goscriber in System Settings → Privacy & Security → Microphone.")
	n.Action = Action{Label: "Open System Settings", ID: ActionOpenMicrophone}
	return n
}

// ModelCorrupt is the notice for a model file that failed to load or verify.
func ModelCorrupt(model string) Notice {
	n := New(CodeModelCorrupt, fmt.Sprintf("The %s model file is damaged. Download it again.", model))
	n.Action = Action{Label: "Download again", ID: ActionOpenModels}
	return n
}

// EngineCrashed is the notice for a speech engine that stopped unexpectedly.
func EngineCrashed(detail string) Notice {
	return New(CodeEngineCrashed, "The speech engine stopped unexpectedly and will restart. "+detail)
}

// EngineCPUFallback is the notice for transcription carrying on on the CPU
// after the GPU failed.
func EngineCPUFallback(detail string) Notice {
	return New(CodeEngineCPUFallback, "The GPU failed, so transcription runs on the CPU, more slowly. "+detail)
}

// TranscriptionFailed is the notice for a recording the speech engine could
// not turn into text, or a model that would not load for a reason other
// than a damaged file.
func TranscriptionFailed(err error) Notice {
	reason := err.Error()
	if eerr, ok := errors.AsType[*engine.Error](err); ok {
		reason = fmt.Sprintf("the speech engine failed to %s", eerr.Op)
		if eerr.Kind != nil {
			reason += fmt.Sprintf(" (%v", eerr.Kind)
			if eerr.Detail != "" {
				reason += ": " + eerr.Detail
			}
			reason += ")"
		}
	}
	return New(CodeTranscriptionFailed, sentence(fmt.Sprintf("Nothing was pasted: %s. Try again.", reason)))
}

// ForPasteError is the notice for a failed delivery: a missing permission
// when the OS refused the key presses, otherwise a failed paste, explained
// by the step that failed rather than the error's package prefixes.
func ForPasteError(err error) Notice {
	if errors.Is(err, output.ErrNoPermission) {
		return AccessibilityMissing()
	}
	reason := err.Error()
	if oerr, ok := errors.AsType[*output.Error](err); ok {
		reason = fmt.Sprintf("the %s step failed (%v)", oerr.Op, oerr.Err)
	}
	return New(CodePasteFailed, sentence(fmt.Sprintf("The transcript could not be pasted: %s. It may still be on the clipboard.", reason)))
}

// ForShortcutError is the notice for a shortcut that could not be
// registered. Without the Accessibility permission (trusted false) the
// permission is the problem to fix.
func ForShortcutError(err error, trusted bool) Notice {
	if !trusted {
		return AccessibilityMissing()
	}
	n := New(CodeShortcutFailed, sentence(fmt.Sprintf("%v. Choose another shortcut in the settings.", err)))
	n.Action = Action{Label: "Open Settings", ID: ActionOpenSettings}
	return n
}

// LoginItemFailed is the notice for the launch at login setting not
// reaching the OS: Goscriber could not be added to the apps that open at
// login (on true), or removed from them.
func LoginItemFailed(on bool, err error) Notice {
	what := "add Goscriber to the apps that open when you log in"
	if !on {
		what = "remove Goscriber from the apps that open when you log in"
	}
	return New(CodeLoginItemFailed, fmt.Sprintf("Could not %s: %v.", what, err))
}

// LoginItemNeedsApproval is the notice for a macOS login item that opens
// Goscriber only once the user allows it in System Settings.
func LoginItemNeedsApproval() Notice {
	n := New(CodeLoginItemFailed, "macOS opens Goscriber at login once you allow it in System Settings → General → Login Items.")
	n.Action = Action{Label: "Open System Settings", ID: ActionOpenLoginItems}
	return n
}

// HookFailed is the notice for a transcription hook that could not run,
// failed or ran too long, so the transcript was pasted as it was.
func HookFailed(err error) Notice {
	reason := "failed: " + err.Error()
	herr, isHook := errors.AsType[*hook.Error](err)
	switch {
	case errors.Is(err, hook.ErrTimeout):
		reason = fmt.Sprintf("ran longer than %d seconds and was stopped", int(hook.DefaultTimeout/time.Second))
	case errors.Is(err, hook.ErrNotExecutable):
		reason = "is not executable: give it permission with chmod +x"
	case errors.Is(err, syscall.ENOEXEC):
		reason = "could not start: a script needs a #! line, such as #!/bin/sh"
	case isHook:
		reason = "failed: " + herr.Reason()
	}
	return New(CodeHookFailed, fmt.Sprintf("The transcription hook %s. The original text was pasted.", clip(reason, hookReasonLen)))
}

// hookReasonLen is the most of a hook's failure a notice shows, in
// characters: the last line of its standard error can be long.
const hookReasonLen = 200

// clip shortens s to n characters, ending it with an ellipsis when it cuts.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// sentence capitalises the first letter of s.
func sentence(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}
