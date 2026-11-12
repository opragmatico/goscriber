package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/opragmatico/goscriber/internal/models"
)

const modelsUsage = `Usage:
  goscriber models list
  goscriber models pull [--quant Q] MODEL
  goscriber models rm MODEL

Commands:
  list   show each model and quantisation, its size and whether it is ready
  pull   download MODEL; the first speech model also brings the voice activity
         detector. An interrupted download resumes where it stopped.
  rm     delete MODEL's files from the models directory (every quantisation)

Flags for pull:
      --quant Q   quantisation to download (default: the model's own, Q8_0)

Nothing is downloaded except by 'pull'. A model already in the Hugging Face
cache (HF_HOME) is used from there once its checksum is verified.
`

func runModels(args []string, env Env) int {
	if len(args) == 0 {
		fmt.Fprint(env.Stderr, modelsUsage)
		return exitUsage
	}
	switch args[0] {
	case "-h", "-help", "--help", "help":
		fmt.Fprint(env.Stdout, modelsUsage)
		return exitOK
	case "list", "ls":
		return modelsList(args[1:], env)
	case "pull":
		return modelsPull(args[1:], env)
	case "rm", "remove":
		return modelsRemove(args[1:], env)
	default:
		fmt.Fprintf(env.Stderr, "goscriber models: unknown command %q\n\n%s", args[0], modelsUsage)
		return exitUsage
	}
}

func modelsList(args []string, env Env) int {
	c := newCommand("models list", modelsUsage, env)
	pos, code, ok := c.parse(args)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		return c.fail("unexpected argument %q", pos[0])
	}
	m, err := env.modelManager()
	if err != nil {
		fmt.Fprintf(env.Stderr, "goscriber: %v\n", err)
		return exitFailure
	}
	tw := tabwriter.NewWriter(env.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "MODEL\tQUANT\tSIZE\tSTATUS")
	for _, st := range m.List() {
		quant := st.File.Quant
		if strings.EqualFold(quant, st.Model.DefaultQuant) && len(st.Model.Files) > 1 {
			quant += " (default)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", st.Model.ID, quant, models.FormatBytes(st.File.Size), describe(st))
	}
	if err := tw.Flush(); err != nil {
		return exitFailure
	}
	fmt.Fprintf(env.Stdout, "\nModels directory: %s\n", m.Dir())
	return exitOK
}

// describe says where a file stands, and what pull would do about it.
func describe(st models.Status) string {
	switch {
	case st.State == models.Ready && st.FromHFCache:
		return "ready (Hugging Face cache)"
	case st.State == models.Downloading:
		return fmt.Sprintf("downloading %d%%", st.Percent())
	case st.State == models.Corrupt:
		return "corrupt (pull replaces it)"
	case st.State == models.NotDownloaded && st.Done > 0:
		return fmt.Sprintf("not downloaded (%d%% fetched; pull resumes)", st.Percent())
	default:
		return st.State.String()
	}
}

func modelsPull(args []string, env Env) int {
	c := newCommand("models pull", modelsUsage, env)
	quant := c.fs.String("quant", "", "quantisation to download")
	pos, code, ok := c.parse(args)
	if !ok {
		return code
	}
	if len(pos) != 1 {
		return c.fail("want one MODEL, got %d arguments", len(pos))
	}
	m, err := env.modelManager()
	if err != nil {
		fmt.Fprintf(env.Stderr, "goscriber: %v\n", err)
		return exitFailure
	}
	id := pos[0]
	if _, err := m.Status(id, *quant); err != nil {
		fmt.Fprintf(env.Stderr, "goscriber: %v\n", err)
		return exitFailure
	}

	ctx, stop := interruptible()
	defer stop()
	progress := make(chan models.Progress, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		showProgress(env.Stderr, progress)
	}()
	err = m.Download(ctx, id, *quant, progress)
	close(progress)
	<-done

	switch {
	case errors.Is(err, context.Canceled):
		fmt.Fprintln(env.Stderr, "goscriber: download interrupted; run the same command to resume")
		return exitInterrupted
	case err != nil:
		fmt.Fprintf(env.Stderr, "goscriber: %v\n", err)
		return exitFailure
	}
	st, err := m.Status(id, *quant)
	if err != nil || st.State != models.Ready {
		fmt.Fprintf(env.Stderr, "goscriber: %s is %v after the download\n", id, st.State)
		return exitFailure
	}
	where := ""
	if st.FromHFCache {
		where = " (Hugging Face cache)"
	}
	fmt.Fprintf(env.Stdout, "%s %s is ready: %s%s\n", st.Model.ID, st.File.Quant, st.Path, where)
	return exitOK
}

// showProgress prints download progress to w until progress is closed: one
// redrawn line on a terminal, otherwise a line per file and per 10%.
func showProgress(w io.Writer, progress <-chan models.Progress) {
	tty := isTerminal(w)
	start := time.Now()
	var startDone int64 = -1
	lastFile, lastStep := "", -1
	drawn := false
	for p := range progress {
		if startDone < 0 {
			startDone = p.Done // bytes resumed, not downloaded now
		}
		pct := 0
		if p.Total > 0 {
			pct = int(p.Done * 100 / p.Total)
		}
		what := "downloading " + p.File
		if p.Verifying {
			what = "verifying " + p.File
		}
		if tty {
			rate := ""
			if secs := time.Since(start).Seconds(); secs >= 1 && p.Done > startDone {
				rate = fmt.Sprintf("  %s/s", models.FormatBytes(int64(float64(p.Done-startDone)/secs)))
			}
			fmt.Fprintf(w, "\r\033[K%s  %s / %s  %d%%%s", what, models.FormatBytes(p.Done), models.FormatBytes(p.Total), pct, rate)
			drawn = true
			continue
		}
		if p.File != lastFile || pct/10 != lastStep || p.Verifying {
			fmt.Fprintf(w, "%s: %s of %s (%d%%)\n", what, models.FormatBytes(p.Done), models.FormatBytes(p.Total), pct)
			lastFile, lastStep = p.File, pct/10
		}
	}
	if drawn {
		fmt.Fprintln(w)
	}
}

func modelsRemove(args []string, env Env) int {
	c := newCommand("models rm", modelsUsage, env)
	pos, code, ok := c.parse(args)
	if !ok {
		return code
	}
	if len(pos) != 1 {
		return c.fail("want one MODEL, got %d arguments", len(pos))
	}
	m, err := env.modelManager()
	if err != nil {
		fmt.Fprintf(env.Stderr, "goscriber: %v\n", err)
		return exitFailure
	}
	id := pos[0]
	switch err := m.Delete(id); {
	case errors.Is(err, models.ErrNotDownloaded):
		fmt.Fprintf(env.Stderr, "goscriber: %s is not in %s\n", id, m.Dir())
		for _, st := range m.List() {
			if st.Model.ID == id && st.FromHFCache {
				fmt.Fprintf(env.Stderr, "goscriber: the copy in the Hugging Face cache stays: %s\n", st.Path)
			}
		}
		return exitFailure
	case err != nil:
		fmt.Fprintf(env.Stderr, "goscriber: %v\n", err)
		return exitFailure
	}
	fmt.Fprintf(env.Stdout, "removed %s\n", id)
	return exitOK
}
