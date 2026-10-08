package main

// progressReader counts bytes through r and, once a second, writes a
// progress line (percentage, bandwidth, ETA) and fires onTick. onTick
// slides conn deadlines — bytes flowing extend the hour, so big
// transfers are bounded by inactivity, not total time. w and now are
// injectable for tests.

import (
	"fmt"
	"io"
	"os"
	"time"
)

type progressReader struct {
	r     io.Reader
	total int64         // 0 = unknown: no percentage, no ETA
	label string        // "sending", "received", "relayed"
	every time.Duration // interval between lines
	w     io.Writer     // progress lines; defaults to stderr
	now   func() time.Time

	rewrite bool // tty: redraw one line with \r instead of appending
	printed bool // a rewritten line is on screen and needs closing

	onTick func(n int64) // deadline sliding; may be nil

	n, lastN int64
	last     time.Time
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.n += int64(n)

	// Counters start at the first byte so connection setup doesn't
	// skew the rate.
	if p.last.IsZero() && p.n > 0 {
		p.last, p.lastN = p.clock(), p.n

		return n, err
	}

	now := p.clock()
	if now.Sub(p.last) >= p.every {
		dt := now.Sub(p.last).Seconds()

		var rate int64

		if dt > 0 {
			rate = int64(float64(p.n-p.lastN) / dt)
		}

		p.emit(p.n, rate)

		p.last, p.lastN = now, p.n
	}

	// EOF closes a rewritten progress line so the next output (the
	// "sent ..." summary) starts clean.
	if err != nil && p.printed {
		p.close()
	}

	return n, err
}

func (p *progressReader) emit(n, rate int64) {
	w, rewrite := p.sink()

	if rewrite {
		_, _ = fmt.Fprintf(w, "\r  %s", progressLine(p.label, n, p.total, rate))
		p.printed = true
	} else {
		_, _ = fmt.Fprintf(w, "  %s\n", progressLine(p.label, n, p.total, rate))
	}

	if p.onTick != nil {
		p.onTick(n)
	}
}

func (p *progressReader) close() {
	if p.printed {
		w, _ := p.sink()
		_, _ = fmt.Fprint(w, "\r\x1b[2K") // done: the line disappears, not lingers
	}

	p.printed = false
}

// sink picks the writer and whether to rewrite: an injected writer
// never rewrites unless told to (tests); bare stderr rewrites only on
// a terminal — the app's pipe pane wants one line per tick.
func (p *progressReader) sink() (io.Writer, bool) {
	if p.w != nil {
		return p.w, p.rewrite
	}

	if p.rewrite || stderrIsTerminal() {
		return os.Stderr, true
	}

	return os.Stderr, false
}

// stderrIsTerminal: char devices are ttys here — no x/term dependency
// for one bit.
func stderrIsTerminal() bool {
	fi, err := os.Stderr.Stat()

	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func (p *progressReader) clock() time.Time {
	if p.now != nil {
		return p.now()
	}

	return time.Now()
}

// progressLine renders one progress step: "sending 512 MiB / 943 MiB
// (54%, 41 MiB/s, eta 11s)", degraded to "relayed 1.2 GiB (38 MiB/s)"
// when the total is unknown.
func progressLine(label string, n, total, rate int64) string {
	s := fmt.Sprintf("%s %s", label, humanBytes(n))

	switch {
	case total > 0 && rate > 0:
		eta := (total - n) / rate
		if eta < 0 {
			eta = 0
		}

		return fmt.Sprintf("%s / %s (%.0f%%, %s/s, eta %s)",
			s, humanBytes(total), 100*float64(n)/float64(total), humanBytes(rate), humanETA(eta))
	case total > 0:
		return fmt.Sprintf("%s / %s (%.0f%%)", s, humanBytes(total), 100*float64(n)/float64(total))
	case rate > 0:
		return fmt.Sprintf("%s (%s/s)", s, humanBytes(rate))
	default:
		return s
	}
}

// humanETA formats a seconds count as 42s, 4m07s, or 3h12m.
func humanETA(sec int64) string {
	switch {
	case sec < 60:
		return fmt.Sprintf("%ds", sec)
	case sec < 3600:
		return fmt.Sprintf("%dm%02ds", sec/60, sec%60)
	default:
		return fmt.Sprintf("%dh%02dm", sec/3600, (sec%3600)/60)
	}
}
