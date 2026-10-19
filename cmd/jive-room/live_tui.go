package main

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/linuxmatters/jive-vocals/internal/cli"
	"github.com/linuxmatters/jive-vocals/internal/live"
)

type liveSnapMsg struct {
	live.Snapshot
}

type liveModel struct {
	mon    live.Monitor
	snap   live.Snapshot
	width  int
	height int
}

func newLiveModel() *liveModel {
	return &liveModel{mon: live.NewMonitor()}
}

func (m *liveModel) Init() tea.Cmd { return nil }

func (m *liveModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case " ", "space":
			m.mon.Freeze(m.snap)
		case "r":
			m.mon.Reset()
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case liveSnapMsg:
		m.snap = msg.Snapshot
		m.mon.Observe(m.snap)
	}
	return m, nil
}

func (m *liveModel) View() tea.View {
	width := m.width
	if width == 0 {
		width = 80
	}
	body := m.mon.Render(m.snap, width)
	view := tea.NewView(body + "\n" + liveHintStyle.Render("relative dBFS, not SPL  ·  freeze before a cut  ·  stop when that band's Δ matches the intended cut"))
	view.AltScreen = true
	return view
}

var liveHintStyle = lipgloss.NewStyle().Foreground(cli.ColorMuted)
