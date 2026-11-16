package ui

import (
	"net/url"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// EngineNotBuiltIn is what the About page says of NeMo-Speech.cpp in a build
// without the speech engine.
const EngineNotBuiltIn = "not built in"

// attribution is a project Goscriber builds on, with its licence.
type attribution struct {
	name, url, license, note string
}

// software is what Goscriber is built with. The models' licences come from
// the catalog.
var software = []attribution{
	{
		"Handy", "https://github.com/cjpais/Handy", "MIT, © CJ Pais",
		"Goscriber's design follows Handy. No Handy source code is copied.",
	},
	{
		"NeMo-Speech.cpp", "https://github.com/NVIDIA/NeMo-Speech.cpp", "Apache-2.0, © NVIDIA",
		"Runs the speech models, with ggml.",
	},
	{"Fyne", "https://fyne.io", "BSD-3-Clause", "The windows, tray and overlay."},
	{"malgo and miniaudio", "https://github.com/gen2brain/malgo", "Public domain (Unlicense)", "Audio capture and the sounds."},
	{"golang.design/x/hotkey", "https://github.com/golang-design/hotkey", "MIT", "Global shortcuts."},
	{"mewkiz/flac", "https://github.com/mewkiz/flac", "Public domain (Unlicense)", "Reading FLAC files to transcribe."},
}

func (w *SettingsWindow) aboutPage() fyne.CanvasObject {
	info := w.opts.Info
	version := container.NewVBox(
		w.row("Version", "", widget.NewLabel(info.Version)),
		w.row("NeMo-Speech.cpp", "", widget.NewLabel(engineLabel(info))),
		w.row("Settings file", "", path(info.SettingsFile)),
		w.row("Logs", "", path(info.LogsDir)),
	)

	credits := container.NewVBox()
	for _, a := range software {
		credits.Add(credit(a))
	}
	modelCredits := container.NewVBox()
	cat := w.catalog()
	for _, m := range cat.Models {
		note := "Speech recognition by NVIDIA."
		if m.ID == cat.VAD().ID {
			note = "Voice activity detection, © Silero Team."
		}
		modelCredits.Add(credit(attribution{m.Name, "https://huggingface.co/" + m.Repo, m.License, note}))
	}

	return settingsPage("Goscriber",
		paragraph("Offline push-to-talk speech-to-text. Audio never leaves your computer."),
		version,
		group("Setup", w.row("First-run setup", "The permissions, the model download and a first dictation, step by step.",
			container.NewHBox(widget.NewButton(LabelRunSetup, func() { w.ShowOnboarding(StepWelcome) })))),
		group("Licence", w.row("Goscriber", "", link("MIT", "https://github.com/opragmatico/goscriber/blob/main/LICENSE"))),
		section("Built with", credits),
		section("Models", paragraph("Downloaded from Hugging Face; none is bundled. Each model card has its terms."), modelCredits),
	)
}

func credit(a attribution) fyne.CanvasObject {
	head := container.NewHBox(link(a.name, a.url), widget.NewLabel("· "+a.license))
	return container.NewVBox(head, note(a.note))
}

func link(text, address string) fyne.CanvasObject {
	u, err := url.Parse(address)
	if err != nil {
		return widget.NewLabel(text)
	}
	return widget.NewHyperlink(text, u)
}
