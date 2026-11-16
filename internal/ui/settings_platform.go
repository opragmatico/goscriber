package ui

import (
	"slices"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/session"
	"github.com/opragmatico/goscriber/internal/typing"
)

// What the settings window says about the desktop and the build: the
// accelerator the engine has, and on Linux the paste keys, the typing tool
// and how Wayland takes the dictation shortcut.

// AcceleratorCPUOnly is the accelerator setting of an engine with no GPU
// backend, such as NeMo-Speech.cpp's cpu-asr build.
const AcceleratorCPUOnly = "The CPU: this build has no GPU support"

// acceleratorSetting offers what the engine has: the GPU of its backend
// and the CPU, or the CPU alone, and then nothing to choose.
func (w *SettingsWindow) acceleratorSetting() (*widget.Select, string) {
	field := func(s *config.Settings) *config.Accelerator { return &s.Accelerator }
	info := w.opts.Info
	switch {
	case info.EngineVersion != "" && info.EngineGPU == "":
		sel := choiceSetting(w, "accelerator", field,
			[]choice[config.Accelerator]{{config.AcceleratorCPU, AcceleratorCPUOnly}},
			func(config.Accelerator) string { return AcceleratorCPUOnly })
		sel.Disable()
		return sel, "Builds with NeMo-Speech.cpp's Vulkan or Metal backend can use the GPU."
	case info.EngineGPU != "":
		return choiceSetting(w, "accelerator", field, []choice[config.Accelerator]{
			{config.AcceleratorAuto, "The GPU when there is one (" + info.EngineGPU + "), else the CPU"},
			{config.AcceleratorCPU, "The CPU only"},
		}, nil), ""
	default: // no engine in this build
		return choiceSetting(w, "accelerator", field, []choice[config.Accelerator]{
			{config.AcceleratorAuto, "The GPU when there is one"},
			{config.AcceleratorCPU, "The CPU only"},
		}, nil), ""
	}
}

// engineLabel is what the About page says of NeMo-Speech.cpp, such as
// "0.3.0 (Vulkan)".
func engineLabel(info SettingsInfo) string {
	switch {
	case info.EngineVersion == "":
		return EngineNotBuiltIn
	case info.EngineGPU == "":
		return info.EngineVersion + " (CPU only)"
	default:
		return info.EngineVersion + " (" + info.EngineGPU + ")"
	}
}

// Paste keys and typing tool choices, Linux only.
var (
	pasteKeysChoices = []choice[config.PasteKeys]{
		{config.PasteShiftInsert, "Shift+Insert (apps and terminals)"},
		{config.PasteCtrlV, "Ctrl+V"},
		{config.PasteCtrlShiftV, "Ctrl+Shift+V (terminals)"},
	}
	typingToolChoices = []choice[config.TypingTool]{
		{config.TypingAuto, "Automatic"},
		{config.TypingXdotool, "xdotool (X11)"},
		{config.TypingWtype, "wtype (Wayland, not GNOME)"},
		{config.TypingDotool, "dotool"},
		{config.TypingYdotool, "ydotool"},
	}
)

// linuxOutputRows are the Output rows only Linux has. Their widgets are
// bound on every system, so the settings file round-trips, but shown on
// Linux only.
func (w *SettingsWindow) linuxOutputRows() []fyne.CanvasObject {
	keys := choiceSetting(w, "paste_keys", func(s *config.Settings) *config.PasteKeys { return &s.PasteKeys },
		pasteKeysChoices, nil)
	tool := choiceSetting(w, "typing_tool", func(s *config.Settings) *config.TypingTool { return &s.TypingTool },
		typingToolChoices, nil)
	if w.opts.Info.os() != "linux" {
		return nil
	}
	return []fyne.CanvasObject{
		w.row("Paste keys", "What pasting through the clipboard presses. Shift+Insert also pastes in terminals and works in any keyboard layout.", keys),
		w.row("Typing tool", typingToolHint(w.opts.Info), tool),
	}
}

// typingToolHint says what Automatic does in this session and which tools
// are installed.
func typingToolHint(info SettingsInfo) string {
	var auto string
	if info.Session == session.Wayland {
		auto = "Automatic uses the first of " + typing.Names(typing.WaylandTools) + " that works. "
	} else {
		auto = "Automatic presses keys itself on X11, with nothing to install. "
	}
	installed := typing.Names(info.TypingTools)
	if installed == "" {
		return auto + "None of the tools is installed."
	}
	return auto + "Installed: " + installed + "."
}

// WaylandShortcutNote is what the General page and onboarding say about
// the dictation shortcut on Wayland when no global shortcuts portal holds
// it.
const WaylandShortcutNote = "Your desktop lets no app take a global shortcut, so add one in its keyboard settings " +
	"(on GNOME: Settings → Keyboard → Keyboard Shortcuts → Custom Shortcuts) with this command: " + ToggleCommand +
	". Each press starts or stops dictation."

// linuxSetupNotes are what a Linux user sets up outside Goscriber: the
// dictation shortcut and a typing tool on Wayland. They show on the
// Permissions page and onboarding's permissions step; elsewhere there are
// none.
func (w *SettingsWindow) linuxSetupNotes() []fyne.CanvasObject {
	info := w.opts.Info
	if info.os() != "linux" {
		return nil
	}
	if info.Session != session.Wayland {
		return []fyne.CanvasObject{paragraph("On X11 Goscriber hears the shortcut and pastes by itself: there is nothing to set up.")}
	}
	tools := "None of them is installed yet: install one, such as ydotool on GNOME."
	if names := typing.Names(info.TypingTools); names != "" {
		tools = "Installed: " + names + "."
	}
	if slices.Equal(info.TypingTools, []config.TypingTool{config.TypingWtype}) {
		tools += " GNOME does not let wtype type: there, install dotool or ydotool."
	}
	return []fyne.CanvasObject{
		widget.NewLabelWithStyle("Dictation shortcut", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		paragraph(w.waylandShortcutNote()),
		widget.NewLabelWithStyle("Pasting", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		paragraph("On Wayland a program presses the paste keys for Goscriber: wtype (Sway, Hyprland, KDE Plasma), " +
			"dotool or ydotool (any desktop, GNOME included, once they may use /dev/uinput). " + tools),
	}
}
