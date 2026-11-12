package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/opragmatico/goscriber/internal/audio"
	"github.com/opragmatico/goscriber/internal/config"
)

const debugUsage = `Usage:
  goscriber debug devices
  goscriber debug record [flags] FILE.wav

Commands:
  devices   list the audio input and output devices, with their IDs
  record    record from the microphone to a 16-bit WAV file, then report the
            sample rate, dropped samples and levels

Flags for record:
      --device D     microphone name or ID (default: the settings' microphone,
                     else the system default)
      --seconds N    how long to record (default 10); Ctrl-C stops early
      --always-on    open the microphone a second before recording and keep
                     its pre-roll, as the always-on setting does
`

func runDebug(args []string, env Env) int {
	if len(args) == 0 {
		fmt.Fprint(env.Stderr, debugUsage)
		return exitUsage
	}
	switch args[0] {
	case "-h", "-help", "--help", "help":
		fmt.Fprint(env.Stdout, debugUsage)
		return exitOK
	case "devices":
		return debugDevices(args[1:], env)
	case "record":
		return debugRecord(args[1:], env)
	default:
		fmt.Fprintf(env.Stderr, "goscriber debug: unknown command %q\n\n%s", args[0], debugUsage)
		return exitUsage
	}
}

func debugDevices(args []string, env Env) int {
	c := newCommand("debug devices", debugUsage, env)
	pos, code, ok := c.parse(args)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		return c.fail("unexpected argument %q", pos[0])
	}
	tw := tabwriter.NewWriter(env.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "KIND\tDEFAULT\tNAME\tID")
	for _, kind := range []struct {
		name string
		list func() ([]audio.Device, error)
	}{{"input", audio.InputDevices}, {"output", audio.OutputDevices}} {
		devs, err := kind.list()
		if err != nil {
			_ = tw.Flush()
			fmt.Fprintf(env.Stderr, "goscriber: %v\n", err)
			return exitFailure
		}
		for _, d := range devs {
			def := ""
			if d.Default {
				def = "*"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", kind.name, def, d.Name, d.ID)
		}
	}
	if err := tw.Flush(); err != nil {
		return exitFailure
	}
	return exitOK
}

func debugRecord(args []string, env Env) int {
	c := newCommand("debug record", debugUsage, env)
	device := c.fs.String("device", "", "microphone name or ID")
	secs := c.fs.Float64("seconds", 10, "seconds to record")
	alwaysOn := c.fs.Bool("always-on", false, "keep pre-roll")
	pos, code, ok := c.parse(args)
	if !ok {
		return code
	}
	if len(pos) != 1 {
		return c.fail("want one FILE.wav, got %d arguments", len(pos))
	}
	if *secs <= 0 || *secs > 3600 {
		return c.fail("--seconds must be between 0 and 3600")
	}
	out := pos[0]
	if *device == "" {
		if paths, err := config.ResolvePaths(); err == nil {
			if s, _, err := config.Load(paths.SettingsFile()); err == nil {
				*device = s.Microphone
			}
		}
	}

	r := audio.NewRecorder(audio.Options{
		Device:     *device,
		AlwaysOn:   *alwaysOn,
		CloseDelay: -1,
		Notify:     func(msg string) { fmt.Fprintf(env.Stderr, "goscriber: %s\n", msg) },
	})
	defer r.Close()
	if *alwaysOn {
		if err := r.Open(); err != nil {
			fmt.Fprintf(env.Stderr, "goscriber: %v\n", err)
			return exitFailure
		}
		time.Sleep(time.Second)
	}

	ctx, stop := interruptible()
	defer stop()
	if err := r.Start(); err != nil {
		fmt.Fprintf(env.Stderr, "goscriber: %v\n", err)
		return exitFailure
	}
	start := time.Now()
	dev, _ := r.Device()
	fmt.Fprintf(env.Stderr, "recording from %q at %d Hz for %.1f s (Ctrl-C stops)\n", dev.Name, r.SampleRate(), *secs)

	// Read Frames as a streaming consumer would, to check it adds up.
	frames := r.Frames()
	streamed := 0
	framesDone := make(chan struct{})
	go func() {
		defer close(framesDone)
		for f := range frames {
			streamed += len(f)
		}
	}()

	tty := isTerminal(env.Stderr)
	timer := time.NewTimer(time.Duration(*secs * float64(time.Second)))
	defer timer.Stop()
	var peak audio.Level
wait:
	for {
		select {
		case l := <-r.Levels():
			peak.Peak = max(peak.Peak, l.Peak)
			if tty {
				fmt.Fprintf(env.Stderr, "\r\033[K%5.1f s  %s %6.1f dBFS", time.Since(start).Seconds(), meterBar(l.RMS), audio.DBFS(l.RMS))
			}
		case <-timer.C:
			break wait
		case <-ctx.Done():
			break wait
		}
	}
	elapsed := time.Since(start)
	samples := r.Stop()
	<-framesDone
	if tty {
		fmt.Fprintln(env.Stderr)
	}
	rate := r.SampleRate()
	if err := audio.WriteWAVFile(out, samples, rate); err != nil {
		fmt.Fprintf(env.Stderr, "goscriber: %v\n", err)
		return exitFailure
	}

	lv := audio.Measure(samples)
	got := time.Duration(float64(len(samples)) / float64(rate) * float64(time.Second))
	dropped := r.Dropped()
	fmt.Fprintf(env.Stdout, "wrote %s: %.3f s from %q at %d Hz\n", out, got.Seconds(), dev.Name, rate)
	fmt.Fprintf(env.Stdout, "samples: %d captured in %.3f s of recording (%s), %d dropped, %d streamed through Frames\n",
		len(samples), elapsed.Seconds(), gapNote(got, elapsed, *alwaysOn), dropped, streamed)
	fmt.Fprintf(env.Stdout, "level: peak %.1f dBFS, RMS %.1f dBFS\n", audio.DBFS(lv.Peak), audio.DBFS(lv.RMS))
	if lv.Peak == 0 {
		fmt.Fprintln(env.Stderr, "goscriber: the recording is silent; check that this terminal may use the microphone (System Settings > Privacy & Security > Microphone on macOS)")
	}
	if dropped > 0 || streamed != len(samples) {
		fmt.Fprintln(env.Stderr, "goscriber: samples were lost")
		return exitFailure
	}
	return exitOK
}

// gapNote compares the audio captured with the time spent recording: the
// device starts a little after Start and pre-roll adds audio before it.
func gapNote(got, elapsed time.Duration, preRoll bool) string {
	note := fmt.Sprintf("%+d ms against the clock", (got - elapsed).Milliseconds())
	if preRoll {
		note += ", pre-roll included"
	}
	return note
}

// meterBar draws an RMS level from -60 to 0 dBFS as 30 characters.
func meterBar(rms float32) string {
	const width = 30
	n := int((audio.DBFS(rms) + 60) / 60 * width)
	n = max(0, min(width, n))
	return "[" + strings.Repeat("#", n) + strings.Repeat(" ", width-n) + "]"
}
