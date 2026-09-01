package output

import (
	"bufio"
	"encoding/json"
	"hash/fnv"
	"io"
	"strings"
	"time"

	"github.com/ochaton/lokigrep/internal/loki"
)

// Printer renders entries to a writer.
type Printer struct {
	w        *bufio.Writer
	format   string // text, raw, json
	color    bool
	utc      bool
	showTime bool
	labels   []string // stream labels rendered next to each line
	tsLayout string
}

const (
	colReset = "\x1b[0m"
	colDim   = "\x1b[2m"
)

// palette avoids red (reserved for errors) and colours that vanish on either
// background.
var palette = []string{
	"\x1b[36m", "\x1b[32m", "\x1b[33m", "\x1b[35m",
	"\x1b[34m", "\x1b[96m", "\x1b[92m", "\x1b[95m",
}

// New returns a printer that renders entries in the given format.
func New(w io.Writer, format string, color, utc, showTime bool, labels []string) *Printer {
	return &Printer{
		w:        bufio.NewWriterSize(w, 64*1024),
		format:   format,
		color:    color,
		utc:      utc,
		showTime: showTime,
		labels:   labels,
		tsLayout: "2006-01-02T15:04:05.000Z07:00",
	}
}

func (p *Printer) Print(e loki.Entry) error {
	switch p.format {
	case "raw":
		return p.writeLine(e.Line)
	case "json":
		return p.writeJSON(e)
	default:
		return p.writeText(e)
	}
}

func (p *Printer) writeText(e loki.Entry) error {
	var b strings.Builder

	if p.showTime {
		t := e.Time()
		if p.utc {
			t = t.UTC()
		} else {
			t = t.Local()
		}
		if p.color {
			b.WriteString(colDim)
		}
		b.WriteString(t.Format(p.tsLayout))
		if p.color {
			b.WriteString(colReset)
		}
		b.WriteByte(' ')
	}

	if label := p.labelString(e); label != "" {
		if p.color {
			b.WriteString(colorFor(label))
		}
		b.WriteString(label)
		if p.color {
			b.WriteString(colReset)
		}
		b.WriteString(" │ ")
	}

	b.WriteString(e.Line)
	return p.writeLine(b.String())
}

func (p *Printer) labelString(e loki.Entry) string {
	parts := make([]string, 0, len(p.labels))
	for _, l := range p.labels {
		if v := e.Labels[l]; v != "" {
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, "/")
}

func (p *Printer) writeJSON(e loki.Entry) error {
	t := e.Time().UTC()
	payload := struct {
		Timestamp string            `json:"timestamp"`
		NS        int64             `json:"ts_ns"`
		Labels    map[string]string `json:"labels"`
		Line      string            `json:"line"`
	}{t.Format(time.RFC3339Nano), e.NS, e.Labels, e.Line}

	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return p.writeLine(string(data))
}

func (p *Printer) writeLine(s string) error {
	if _, err := p.w.WriteString(s); err != nil {
		return err
	}
	return p.w.WriteByte('\n')
}

func (p *Printer) Flush() error { return p.w.Flush() }

func colorFor(s string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return palette[int(h.Sum32())%len(palette)]
}
