package progress

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	pb "github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ochaton/lokigrep/internal/loki"
)

// frac is how much of the scanned range is behind us, clamped to [0, 1].
func frac(s loki.ScanStat) float64 {
	if s.Total <= 0 {
		return 1
	}
	f := float64(s.Done) / float64(s.Total)
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

// Progress is a scan indicator drawn on stderr. It lives only until the first
// line reaches stdout, so it never interleaves with output. Every method is
// safe on a nil receiver, which is what Start returns when stderr is
// not a terminal.
type Progress struct {
	prog *tea.Program
	done chan struct{}
	once sync.Once
}

// Start draws a scan indicator for the range [from, to) on stderr.
func Start(from, to time.Time, label string) *Progress {
	if !stderrIsTTY() {
		return nil
	}
	total := to.Sub(from).Nanoseconds()
	m := progModel{
		bar:   pb.New(pb.WithDefaultGradient(), pb.WithoutPercentage()),
		spin:  spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		label: label,
		from:  from,
		to:    to,
		start: time.Now(),
		width: 80,
		stat:  loki.ScanStat{Total: total, At: from},
	}

	prog := tea.NewProgram(m,
		tea.WithOutput(os.Stderr),
		tea.WithInput(nil),
		tea.WithoutSignalHandler(),
	)
	p := &Progress{prog: prog, done: make(chan struct{})}
	go func() {
		defer close(p.done)
		_, _ = prog.Run()
	}()
	return p
}

// Update reports how far the scan got.
func (p *Progress) Update(s loki.ScanStat) {
	if p == nil {
		return
	}
	p.prog.Send(statMsg(s))
}

// Following switches the indicator to the indeterminate "waiting for new
// lines" state used by -f once the historical scan is done.
func (p *Progress) Following(since time.Time) {
	if p == nil {
		return
	}
	p.prog.Send(followMsg{since: since})
}

// Println writes a line above the indicator, so a warning raised mid-scan does
// not fight the bar for the last line of the terminal.
func (p *Progress) Println(s string) {
	if p == nil {
		_, _ = fmt.Fprintf(os.Stderr, "%s\n", s)
		return
	}
	p.prog.Println(s)
}

// Stop tears the indicator down and clears its line. Safe to call twice.
func (p *Progress) Stop() {
	if p == nil {
		return
	}
	p.once.Do(func() {
		p.prog.Quit()
		<-p.done
	})
}

type statMsg loki.ScanStat

type followMsg struct{ since time.Time }

type progModel struct {
	bar   pb.Model
	spin  spinner.Model
	label string
	from  time.Time
	to    time.Time
	start time.Time

	stat      loki.ScanStat
	following bool
	since     time.Time
	width     int
	quitting  bool
}

func (m progModel) Init() tea.Cmd { return m.spin.Tick }

func (m progModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 { // a pty that cannot tell us its size reports 0
			m.width = msg.Width
		}
		return m, nil
	case statMsg:
		m.stat = loki.ScanStat(msg)
		return m, nil
	case followMsg:
		m.following = true
		m.since = msg.since
		return m, nil
	case tea.QuitMsg:
		m.quitting = true
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	}
	return m, nil
}

var (
	dimStyle  = lipgloss.NewStyle().Faint(true)
	spinStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
)

func (m progModel) View() string {
	if m.quitting {
		return ""
	}

	elapsed := time.Since(m.start).Truncate(100 * time.Millisecond)
	counts := fmt.Sprintf("%s lines · %d req · %s",
		humanCount(m.stat.Entries), m.stat.Requests, elapsed)

	if m.following {
		return spinStyle.Render(m.spin.View()) + " " +
			dimStyle.Render(fmt.Sprintf("following since %s · no match yet · %s",
				m.since.Local().Format("15:04:05"), counts))
	}

	head := fmt.Sprintf("%s %s", m.label, m.stat.At.Local().Format("15:04:05"))
	tail := counts
	if eta := m.eta(); eta != "" {
		tail += " · " + eta
	}
	pct := fmt.Sprintf("%3.0f%%", frac(m.stat)*100)

	// spinner + head + bar + pct + tail, with two spaces between each field
	fixed := lipgloss.Width(m.spin.View()) + lipgloss.Width(head) +
		lipgloss.Width(pct) + lipgloss.Width(tail) + 8
	barWidth := m.width - fixed
	if barWidth < 8 {
		// too narrow for a bar: drop it and keep the numbers
		line := spinStyle.Render(m.spin.View()) + " " + head + " " + dimStyle.Render(pct+" "+tail)
		return truncateCells(line, m.width)
	}
	if barWidth > 40 {
		barWidth = 40
	}
	m.bar.Width = barWidth

	return strings.Join([]string{
		spinStyle.Render(m.spin.View()),
		head,
		m.bar.ViewAs(frac(m.stat)),
		pct,
		dimStyle.Render(tail),
	}, " ")
}

// eta extrapolates the remaining time from how much of the range is done. It
// stays quiet until enough of the scan is behind us to mean anything.
func (m progModel) eta() string {
	f := frac(m.stat)
	if f < 0.05 || f >= 1 {
		return ""
	}
	elapsed := time.Since(m.start)
	if elapsed < time.Second {
		return ""
	}
	left := time.Duration(float64(elapsed) * (1 - f) / f)
	return "~" + left.Truncate(time.Second).String() + " left"
}

func humanCount(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 1_000_000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
}

func truncateCells(s string, width int) string {
	if width <= 0 || lipgloss.Width(s) <= width {
		return s
	}
	return string([]rune(s)[:width])
}

func stderrIsTTY() bool {
	st, err := os.Stderr.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}
