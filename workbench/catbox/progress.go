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

// transferPace, when non-zero, sleeps this long after each chunk of
// sealed-stream bytes read — a test seam (CATBOX_PACE): transfer
// duration becomes chunks × pace, deterministic on any network, so
// the integration kill windows do not depend on payload size.
var transferPace time.Duration

func init() {
	if v := os.Getenv("CATBOX_PACE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			transferPace = d
		}
	}
}

type progressReader struct {
	r      io.Reader
	total  int64         // 0 = unknown: no percentage, no ETA
	offset int64         // resume point: the counter starts here, wire bytes add on top
	label  string        // "sending", "received", "relayed"
	every  time.Duration // interval between lines
	path   func() string // live network path; may be nil
	w      io.Writer     // progress lines; defaults to stderr
	now    func() time.Time

	rewrite bool // tty: redraw one line with \r instead of appending
	printed bool // a rewritten line is on screen and needs closing

	onTick func(n int64) // deadline sliding; may be nil

	n, lastN int64
	last     time.Time
	paced    int64 // bytes counted toward the next transferPace sleep
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.n += int64(n)

	// The pacing seam: a fixed per-chunk sleep makes the transfer's
	// duration deterministic on any network.
	if transferPace > 0 {
		p.paced += int64(n)

		for p.paced >= chunkSize {
			p.paced -= chunkSize

			time.Sleep(transferPace)
		}
	}

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

		p.emit(p.offset+p.n, rate)

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

	var path string
	if p.path != nil {
		path = p.path()
	}

	if rewrite {
		_, _ = fmt.Fprintf(w, "\r  %s", progressLine(p.label, n, p.total, rate, path, true))
		p.printed = true
	} else {
		_, _ = fmt.Fprintf(w, "  %s\n", progressLine(p.label, n, p.total, rate, path, false))
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

// progressLine renders one progress step: "sending [████░░] 78.6 MiB
// / 418.3 MiB (19%), 20.0 MiB/s, path: direct 192.0.2.1:41414" — the
// bar only on a terminal, the numbers absolute, the path naming the
// connection. Degraded to "relayed 1.2 GiB (38 MiB/s)" when the total
// is unknown.
func progressLine(label string, n, total, rate int64, path string, bar bool) string {
	if path != "" {
		path = ", path: " + path
	}

	var s string
	if bar && total > 0 {
		s = fmt.Sprintf("%s %s %s", label, barCells(n, total), humanBytes(n))
	} else {
		s = fmt.Sprintf("%s %s", label, humanBytes(n))
	}

	switch {
	case total > 0 && rate > 0:
		return fmt.Sprintf("%s / %s (%.0f%%), %s/s%s",
			s, humanBytes(total), 100*float64(n)/float64(total), humanBytes(rate), path)
	case total > 0:
		return fmt.Sprintf("%s / %s (%.0f%%%s)", s, humanBytes(total), 100*float64(n)/float64(total), path)
	case rate > 0:
		return fmt.Sprintf("%s (%s/s%s)", s, humanBytes(rate), path)
	default:
		return s + path
	}
}

// barCells renders a fixed-width unicode bar: n of total.
func barCells(n, total int64) string {
	const width = 24

	cells := make([]rune, width)
	for i := range cells {
		cells[i] = '░'
	}

	if total > 0 {
		filled := int(float64(n) / float64(total) * float64(width))
		if filled > width {
			filled = width
		}

		for i := range filled {
			cells[i] = '█'
		}
	}

	return "[" + string(cells) + "]"
}
