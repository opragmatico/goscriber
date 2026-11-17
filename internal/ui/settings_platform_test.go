package ui

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/session"
)

// The accelerator setting offers what the engine has: a CPU-only engine
// (NeMo-Speech.cpp's cpu-asr build, the Linux default) leaves nothing to
// choose, and a GPU build names its backend.
func TestAcceleratorOffersWhatTheEngineHas(t *testing.T) {
	cpuOnly := newTestWindow(t, func(o *SettingsOptions) { o.Info.EngineGPU = "" })
	cpuOnly.show(PageAdvanced)
	sel := cpuOnly.bound(t, "accelerator").(*widget.Select)
	if !sel.Disabled() || sel.Selected != AcceleratorCPUOnly || !slices.Contains(sel.Options, AcceleratorCPUOnly) {
		t.Errorf("CPU-only engine: options %q, selected %q, disabled %v; want %q alone, disabled",
			sel.Options, sel.Selected, sel.Disabled(), AcceleratorCPUOnly)
	}
	if got := cpuOnly.pageText(PageAbout); !strings.Contains(got, "0.3.0 (CPU only)") {
		t.Errorf("About says %q, want the engine marked CPU only", got)
	}

	vulkan := newTestWindow(t, func(o *SettingsOptions) { o.Info.EngineGPU = "Vulkan" })
	vulkan.show(PageAdvanced)
	sel = vulkan.bound(t, "accelerator").(*widget.Select)
	want := []string{"The GPU when there is one (Vulkan), else the CPU", "The CPU only"}
	if sel.Disabled() || !slices.Equal(sel.Options, want) || sel.Selected != want[0] {
		t.Errorf("Vulkan engine: options %q, selected %q, disabled %v; want %q", sel.Options, sel.Selected, sel.Disabled(), want)
	}
	sel.SetSelected("The CPU only")
	if got := vulkan.store.Get().Accelerator; got != config.AcceleratorCPU {
		t.Errorf("choosing the CPU saved %q", got)
	}
	if got := vulkan.pageText(PageAbout); !strings.Contains(got, "0.3.0 (Vulkan)") {
		t.Errorf("About says %q, want the engine's backend", got)
	}
}

// The paste keys and typing tool are Linux settings: shown there, with the
// tools that are installed, and nowhere else.
func TestLinuxOutputRows(t *testing.T) {
	mac := newTestWindow(t, func(o *SettingsOptions) { o.Info.OS = "darwin" })
	mac.show(PageAdvanced)
	if got := mac.pageText(PageAdvanced); strings.Contains(got, "Typing tool") || strings.Contains(got, "Paste keys") {
		t.Errorf("macOS shows Linux's output settings: %q", got)
	}

	linux := newTestWindow(t, func(o *SettingsOptions) {
		o.Info.OS = "linux"
		o.Info.Session = session.Wayland
		o.Info.TypingTools = []config.TypingTool{config.TypingWtype, config.TypingYdotool}
	})
	linux.show(PageAdvanced)
	got := linux.pageText(PageAdvanced)
	for _, want := range []string{"Paste keys", "Typing tool", "Automatic uses the first of wtype, dotool or ydotool", "Installed: wtype or ydotool."} {
		if !strings.Contains(got, want) {
			t.Errorf("Linux's Advanced page lacks %q: %q", want, got)
		}
	}
	linux.bound(t, "typing_tool").(*widget.Select).SetSelected("ydotool")
	linux.bound(t, "paste_keys").(*widget.Select).SetSelected("Ctrl+V")
	if s := linux.store.Get(); s.TypingTool != config.TypingYdotool || s.PasteKeys != config.PasteCtrlV {
		t.Errorf("saved typing tool %q and paste keys %q", s.TypingTool, s.PasteKeys)
	}

	x11 := newTestWindow(t, func(o *SettingsOptions) { o.Info.OS = "linux"; o.Info.Session = session.X11 })
	x11.show(PageAdvanced)
	if got := x11.pageText(PageAdvanced); !strings.Contains(got, "presses keys itself on X11") || !strings.Contains(got, "None of the tools is installed.") {
		t.Errorf("X11's typing tool hint: %q", got)
	}
}

// On Wayland the desktop takes the dictation shortcut: the General page,
// the Permissions page and onboarding say how to set it up, with the
// command to bind.
func TestWaylandShortcutSetup(t *testing.T) {
	tw := newTestWindow(t, func(o *SettingsOptions) {
		o.Info.OS = "linux"
		o.Info.Session = session.Wayland
		o.Info.TypingTools = []config.TypingTool{config.TypingWtype}
	})
	tw.show(PageGeneral)
	if got := tw.pageText(PageGeneral); !strings.Contains(got, ToggleCommand) {
		t.Errorf("General page %q lacks the command to bind", got)
	}
	tw.show(PagePermissions)
	got := tw.pageText(PagePermissions)
	for _, want := range []string{PermissionsNone, ToggleCommand, "GNOME does not let wtype type"} {
		if !strings.Contains(got, want) {
			t.Errorf("Permissions page lacks %q: %q", want, got)
		}
	}

	tw.showOnboarding(StepTry)
	if got := tw.stepText(); !strings.Contains(got, TryHintWayland) || !strings.Contains(got, ToggleCommand) {
		t.Errorf("the try step %q does not say how to dictate on Wayland", got)
	}

	x11 := newTestWindow(t, func(o *SettingsOptions) { o.Info.OS = "linux"; o.Info.Session = session.X11 })
	x11.show(PageGeneral)
	if got := x11.pageText(PageGeneral); strings.Contains(got, ToggleCommand) {
		t.Errorf("X11's General page mentions the Wayland command: %q", got)
	}
	x11.show(PagePermissions)
	if got := x11.pageText(PagePermissions); !strings.Contains(got, "nothing to set up") {
		t.Errorf("X11's Permissions page: %q", got)
	}
	mac := newTestWindow(t, func(o *SettingsOptions) { o.Info.OS = "darwin"; o.Info.Session = session.Native })
	mac.show(PagePermissions)
	if got := mac.pageText(PagePermissions); strings.Contains(got, "X11") || strings.Contains(got, "Wayland") {
		t.Errorf("macOS's Permissions page talks about Linux: %q", got)
	}
}

// visibleText joins the text of the labels and buttons in o that show,
// leaving out what is hidden along with everything inside it.
func visibleText(o fyne.CanvasObject) string {
	var out []string
	var walk func(o fyne.CanvasObject)
	walk = func(o fyne.CanvasObject) {
		if !o.Visible() {
			return
		}
		switch x := o.(type) {
		case *widget.Label:
			out = append(out, x.Text)
		case *widget.Button:
			out = append(out, "["+x.Text+"]")
		case *fyne.Container:
			for _, c := range x.Objects {
				walk(c)
			}
		case fyne.Widget:
			for _, c := range test.WidgetRenderer(x).Objects() {
				walk(c)
			}
		}
	}
	walk(o)
	return strings.Join(out, "\n")
}

// When the desktop's global shortcuts portal holds the shortcuts, the
// General page shows the keys it bound instead of the command to bind,
// and follows what the portal says.
func TestDesktopShortcutsFromThePortal(t *testing.T) {
	changed := 0
	tw := newTestWindow(t, func(o *SettingsOptions) {
		o.Info.OS = "linux"
		o.Info.Session = session.Wayland
		o.ChangeDesktopShortcuts = func() error {
			changed++
			if changed > 1 {
				return errors.New("the portal is gone")
			}
			return nil
		}
	})
	tw.show(PageGeneral)
	general := func() string { return visibleText(tw.items[PageGeneral].Content) }
	if got := general(); !strings.Contains(got, ToggleCommand) || strings.Contains(got, "Desktop keys") {
		t.Errorf("before the portal answers, the General page should say how to bind the command:\n%s", got)
	}

	tw.SetDesktopShortcuts(DesktopShortcuts{State: PortalAsking})
	got := general()
	for _, want := range []string{"Desktop keys", "Waiting for you to confirm", DictationHintPortal, CancelHintPortal} {
		if !strings.Contains(got, want) {
			t.Errorf("while asking, the General page lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, ToggleCommand) || strings.Contains(got, "[Change…]") {
		t.Errorf("while asking, the General page offers the command or Change…:\n%s", got)
	}

	tw.SetDesktopShortcuts(DesktopShortcuts{State: PortalBound, Transcribe: "Ctrl+Alt+Space", Cancel: "Ctrl+Escape", CanChange: true})
	if got := general(); !strings.Contains(got, "Dictation: Ctrl+Alt+Space. Cancel: Ctrl+Escape.") || !strings.Contains(got, "[Change…]") {
		t.Errorf("once bound:\n%s", got)
	}
	test.Tap(tw.buttons(PageGeneral)["Change…"])
	test.Tap(tw.buttons(PageGeneral)["Change…"])
	if changed != 2 {
		t.Errorf("Change… called the app %d times", changed)
	}
	if got := general(); !strings.Contains(got, "The portal is gone.") {
		t.Errorf("a failed change is not shown:\n%s", got)
	}

	// Version 1 of the portal cannot change the keys: the desktop's
	// settings do.
	tw.SetDesktopShortcuts(DesktopShortcuts{State: PortalBound, Transcribe: "Ctrl+Alt+Space"})
	got = general()
	if !strings.Contains(got, "Cancel: no keys. Change them in the desktop's keyboard settings.") || strings.Contains(got, "[Change…]") {
		t.Errorf("bound by version 1:\n%s", got)
	}
	tw.show(PagePermissions)
	if got := tw.pageText(PagePermissions); !strings.Contains(got, "global shortcuts portal") || strings.Contains(got, ToggleCommand) {
		t.Errorf("Permissions page once bound: %q", got)
	}
	tw.showOnboarding(StepTry)
	if got := tw.stepText(); !strings.Contains(got, "Ctrl+Alt+Space") || strings.Contains(got, ToggleCommand) {
		t.Errorf("the try step once bound: %q", got)
	}

	// A dismissed dialog leaves the command to bind.
	tw.SetDesktopShortcuts(DesktopShortcuts{State: PortalRefused, Problem: "dismissed"})
	if got := tw.stepText(); !strings.Contains(got, ToggleCommand) {
		t.Errorf("the try step after a refusal: %q", got)
	}
	tw.endOnboarding(true)
	tw.show(PageGeneral)
	if got := general(); !strings.Contains(got, "You dismissed the desktop's dialog") || !strings.Contains(got, ToggleCommand) {
		t.Errorf("after a refusal:\n%s", got)
	}
}

// Launch at login is a check box that saves the setting, unless there can
// be no login item, as from a bare executable on macOS: then it is
// disabled, and says why.
func TestLaunchAtLoginSetting(t *testing.T) {
	tw := newTestWindow(t, nil)
	tw.show(PageAdvanced)
	c := tw.bound(t, "autostart").(*widget.Check)
	if c.Disabled() || !strings.Contains(tw.pageText(PageAdvanced), LaunchAtLoginHint) {
		t.Errorf("disabled %v; page %q", c.Disabled(), tw.pageText(PageAdvanced))
	}
	test.Tap(c)
	if !tw.store.Get().Autostart {
		t.Error("ticking launch at login did not save it")
	}

	bare := newTestWindow(t, func(o *SettingsOptions) {
		o.Info.LoginItemUnavailable = "only Goscriber.app can open at login, not a bare executable"
	})
	bare.show(PageAdvanced)
	c = bare.bound(t, "autostart").(*widget.Check)
	text := bare.pageText(PageAdvanced)
	if !c.Disabled() || !strings.Contains(text, "Only Goscriber.app can open at login, not a bare executable.") || strings.Contains(text, LaunchAtLoginHint) {
		t.Errorf("disabled %v; page %q", c.Disabled(), text)
	}
}
