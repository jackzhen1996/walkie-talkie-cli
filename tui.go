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
			Foreground(lipgloss.Color("205")).MarginBottom(2)

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			MarginTop(1)
)

// ----- Model -----
type model struct {
	name               string
	recorder           *Recorder
	peer               *Peer
	isRecording        bool
	isPeerTalking      bool
	isEstablishingPeer bool
	peerIds            []string
	chatHistory        []string
}

// Indicate that the other peer is talking
type PeerTalkingNotification struct {
	isPeerTalking bool
}

func initialModel(recorder *Recorder, peer *Peer, name string) model {
	return model{
		name:               name,
		recorder:           recorder,
		peer:               peer,
		isRecording:        false,
		isEstablishingPeer: true,
		isPeerTalking:      false,
		peerIds:            []string{"peerA", "peerB"},
	}
}

func setupAudioRecorderAndPeer(name string) tea.Msg {
	return InitPeer(name)
}

func setPeerTalking(p *tea.Program, isPeerTalking bool) {
	p.Send(PeerTalkingNotification{isPeerTalking: isPeerTalking})
}

// Init runs once when the program starts.
func (m model) Init() tea.Cmd {
	return func() tea.Msg {
		return setupAudioRecorderAndPeer(m.name)
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	// Handle initialization of peer
	case *Peer:
		m.peer = msg
		m.isEstablishingPeer = false
		return m, nil

	case PeerTalkingNotification:
		m.isPeerTalking = msg.isPeerTalking
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		// Handle shutdowns
		case "q", "ctrl+c":
			m.peer.PeerCleanUp()
			return m, tea.Quit
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
	title := titleStyle.Render(fmt.Sprintf("Welcome to Walkie Talkie, %s!", m.name), " ", " ")
	if m.isEstablishingPeer {
		return lipgloss.JoinVertical(lipgloss.Left, title, "Establishing Connection with peer....")
	}

	members := fmt.Sprintf("You are chatting with: %s", m.peer.otherPeer)
	recordIndicator := "Press space to say something..."
	if m.isRecording {
		recordIndicator = "Recording...press space again to stop recording"
	}

	peerTalkingIndicator := " "
	if m.isPeerTalking {
		peerTalkingIndicator = fmt.Sprintf("%s is talking...", m.peer.otherPeer)
	}

	helper := helpStyle.Render("q: quit")
	return lipgloss.JoinVertical(lipgloss.Left, title, members, peerTalkingIndicator, recordIndicator, helper)
}
