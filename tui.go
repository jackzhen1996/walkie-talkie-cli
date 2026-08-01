package main

import (
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ----- Styles -----
var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("205"))

	selectedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("42"))

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			MarginTop(1)
)

// ----- Model -----
type model struct {
	recorder      *Recorder
	peer          *Peer
	isRecording   bool
	isPeerTalking bool
	peerIds       []string
	chatHistory   []string
}

func initialModel(recorder *Recorder, peer *Peer) model {
	return model{
		recorder:    recorder,
		peer:        peer,
		isRecording: false,
		peerIds:     []string{"peerA", "peerB"},
	}
}

// Init runs once when the program starts.
func (m model) Init() tea.Cmd {
	return nil
}

// Update handles incoming messages (keypresses, custom events, etc.)
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case " ":
			if m.isRecording {
				m.recorder.StopRecording()
				if err := m.peer.streamPCMBytes(*m.recorder.data, int(m.recorder.sampleRate), 1); err != nil {
					fmt.Errorf("Error streaming recorded data to peer", err)
				}
				m.recorder.ClearAudioBuffer()
				m.isRecording = false
			} else {
				// start record, start the recording goroutine
				if err := m.recorder.StartRecording(); err != nil {
					fmt.Errorf("Failed to record audio")
					return m, nil
				}

				m.isRecording = true
			}
		}
	}
	return m, nil
}

// View renders the UI as a string.
func (m model) View() string {
	s := titleStyle.Render("Welcome to Walkie Talkie! \n\n")

	recordIndicator := "Press space to say something...\n"
	if m.isRecording {
		recordIndicator = "Recording...press space again to stop recording\n"
	}

	peerTalkingIndicator := ""
	if m.isPeerTalking {
		peerTalkingIndicator = "peer is talking...."
	}

	helper := helpStyle.Render("↑/↓ or j/k: move • enter/space: toggle • q: quit")
	return lipgloss.JoinVertical(lipgloss.Left, s, peerTalkingIndicator, recordIndicator, helper)
}
