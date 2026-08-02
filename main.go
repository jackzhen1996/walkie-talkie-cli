package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/alexflint/go-arg"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ebitengine/oto/v3"
	"github.com/hraban/opus"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/pion/webrtc/v4/pkg/media/oggreader"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

// audioOutput is the process-wide Oto context. Oto only allows ONE context
// per process, so this is created once in main() and shared by whichever
// track(s) call readIncomingAudio.
var audioOutput *oto.Context
var signalingBaseURL string
var peerName string
var tui *tea.Program

type PeerSignalPayload struct {
	PeerName      string                    `json:"peerName"`
	SignalPayload webrtc.SessionDescription `json:"signalPayload"`
}

type Peer struct {
	name       string
	pc         *webrtc.PeerConnection
	localTrack *webrtc.TrackLocalStaticSample
	otherPeer  string
	isOfferer  bool
}

type CommandLineArgs struct {
	PeerName         string `arg:"--name" help:"Peer name"`
	SignalingBaseUrl string `arg:"--signalUrl" help:"Signaling Server URL. Example: http://localhost:8080"`
}

func InitPeer(name string) *Peer {
	newPeer := &Peer{name: name, isOfferer: false}
	pc, localTrack, err := newPeer.setupPeerConnection()
	if err != nil {
		log.Fatal(err)
	}
	newPeer.localTrack = localTrack
	newPeer.pc = pc

	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		if state == webrtc.ICEConnectionStateConnected {
		}
	})

	if err := newPeer.tryNegotiatePeer(); err != nil {
		log.Fatal(err)
	}

	// start the background process of checking for disconnection
	go func() {
		for {
			checkEndpoint := "/offer"
			if newPeer.isOfferer {
				checkEndpoint = "/answer"
			}

			resp, _ := http.Get(signalingBaseURL + checkEndpoint)
			if resp.StatusCode == http.StatusNotFound {
				setPeerDisconnection(tui)
				return
			}
			time.Sleep(time.Second * 10)
		}
	}()

	return newPeer

}

func (p *Peer) PeerCleanUp() {
	fmt.Println(fmt.Sprintf("%s's cli shutting down...", p.name))
	if err := p.pc.Close(); err != nil {
		fmt.Println(fmt.Sprintf("%s: error closing peer connection:", p.name), err)
	}
	time.Sleep(500 * time.Millisecond) // give readIncomingAudio a moment to clean up

	url := signalingBaseURL
	if p.isOfferer {
		url += "/offer"
	} else {
		url += "/answer"
	}

	req, _ := http.NewRequest(http.MethodDelete, url, nil)
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Panic(err)
	}
	resp.Body.Close()
}

func main() {
	var peer *Peer
	var args CommandLineArgs

	// default args
	args.PeerName = "A"
	args.SignalingBaseUrl = "http://localhost:8080"
	arg.MustParse(&args)

	signalingBaseURL = args.SignalingBaseUrl
	peerName = args.PeerName

	// Initialize audio capture stuff
	output := initAudioOutput()
	audioOutput = output
	recorder := InitRecorder()

	p := tea.NewProgram(initialModel(recorder, peer, peerName))
	tui = p
	if _, err := p.Run(); err != nil {
		fmt.Println("Error running program:", err)
		os.Exit(1)
	}
}

// initAudioOutput creates the process-wide Oto context used for playback.
// Must be called exactly once - Oto does not support multiple contexts.
func initAudioOutput() *oto.Context {
	op := &oto.NewContextOptions{
		SampleRate:   48000,
		ChannelCount: 1, // matches our mono Opus track
		Format:       oto.FormatSignedInt16LE,
		BufferSize:   40 * time.Millisecond, // small buffer to keep latency down
	}
	ctx, ready, err := oto.NewContext(op)
	if err != nil {
		fmt.Println("Failed to initialize audio context", err)
		os.Exit(1)
	}
	<-ready // wait for the audio device to be ready
	return ctx
}

// setupPeerConnection builds a PeerConnection with one outbound audio track
// and a handler for whatever inbound audio track the other peer sends.
func (p *Peer) setupPeerConnection() (*webrtc.PeerConnection, *webrtc.TrackLocalStaticSample, error) {
	m := &webrtc.MediaEngine{}
	if err := m.RegisterDefaultCodecs(); err != nil {
		return nil, nil, err
	}
	api := webrtc.NewAPI(webrtc.WithMediaEngine(m))

	pc, err := api.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
		},
	})
	if err != nil {
		return nil, nil, err
	}

	localTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "audio", p.name,
	)
	if err != nil {
		return nil, nil, err
	}

	rtpSender, err := pc.AddTrack(localTrack)
	if err != nil {
		return nil, nil, err
	}

	// Drain RTCP so the underlying buffers don't fill up.
	go func() {
		buf := make([]byte, 1500)
		for {
			if _, _, err := rtpSender.Read(buf); err != nil {
				return
			}
		}
	}()

	pc.OnTrack(func(remoteTrack *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		//fmt.Println(fmt.Sprintf("%s receiving remote track:", p.peerId), remoteTrack.Codec().MimeType)
		go readIncomingAudio(remoteTrack)
	})

	return pc, localTrack, nil
}

// readIncomingAudio drains RTP packets from the remote track, decodes each
// one's Opus payload to raw PCM in-process (github.com/hraban/opus, a cgo
// binding to libopus), and streams the PCM straight to the OS audio device
// via oto (github.com/ebitengine/oto/v3) - no subprocess, no container
// format, no file on disk.
func readIncomingAudio(track *webrtc.TrackRemote) {
	decoder, err := opus.NewDecoder(48000, 1) // must match the track's sample rate/channels
	if err != nil {
		fmt.Println("Peer could not create opus decoder:", err)
		return
	}

	// oto.Player reads from an io.Reader, so we hand it the read end of a
	// pipe and write decoded PCM into the write end as it arrives.
	pipeReader, pipeWriter := io.Pipe()
	player := audioOutput.NewPlayer(pipeReader)
	player.Play()
	defer player.Close()

	// 5760 = 48000Hz * 120ms, the largest legal Opus frame. Decode() only
	// fills however many samples the frame actually contains.
	var lastReceivedAudioTs time.Time

	// Jank way of updating the UI to remove the talking indicator
	go func() {
		for {
			if time.Now().Sub(lastReceivedAudioTs) > time.Millisecond*500 {
				setPeerTalking(tui, false)
			}
			time.Sleep(time.Second * 1)
		}
	}()

	pcm := make([]int16, 5760)
	for {
		rtpPacket, _, err := track.ReadRTP()

		if err != nil {
			pipeWriter.Close()
			return
		}

		n, err := decoder.Decode(rtpPacket.Payload, pcm)
		// In case a packet is corrupted and can't be read, we still wanna show the talking indicator
		setPeerTalking(tui, true)
		lastReceivedAudioTs = time.Now()

		if err != nil {
			fmt.Println("Peer opus decode error, skipping packet:", err)
			continue
		}

		if _, err := pipeWriter.Write(int16SamplesToBytes(pcm[:n])); err != nil {
			fmt.Println("Peer error writing PCM to player:", err)
			return
		}
	}

}

// int16SamplesToBytes converts decoded PCM samples into the little-endian
// byte layout oto expects (FormatSignedInt16LE).
func int16SamplesToBytes(samples []int16) []byte {
	buf := make([]byte, len(samples)*2)
	for i, s := range samples {
		binary.LittleEndian.PutUint16(buf[i*2:], uint16(s))
	}
	return buf
}

func openAudioFile(filename string) (*os.File, error) {
	return os.Open(filename)
}

func bytesToInt16Samples(raw []byte) ([]int16, error) {
	if len(raw)%2 != 0 {
		return nil, fmt.Errorf("odd byte count %d: 16-bit PCM must be a multiple of 2 bytes", len(raw))
	}
	samples := make([]int16, len(raw)/2)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(raw[i*2:]))
	}
	return samples, nil
}

func (p *Peer) streamPCMBytes(raw []byte, sampleRate, channels int) error {
	samples, _ := bytesToInt16Samples(raw)

	var maxAbs int16
	nonZero := 0
	for _, s := range samples {
		if s != 0 {
			nonZero++
		}
		abs := s
		if abs < 0 {
			abs = -abs
		}
		if abs > maxAbs {
			maxAbs = abs
		}
	}
	//fmt.Printf("total samples: %d, non-zero: %d, max amplitude: %d\n",
	//	len(samples), nonZero, maxAbs)
	return p.streamPCM(samples, sampleRate, channels)
}

func (p *Peer) streamPCM(pcm []int16, sampleRate, channels int) error {
	encoder, err := opus.NewEncoder(sampleRate, channels, opus.AppVoIP)
	if err != nil {
		return fmt.Errorf("creating opus encoder: %w", err)
	}

	// Opus frames must be an exact legal duration: 2.5/5/10/20/40/60ms.
	// 20ms matches what the rest of this example (and Opus in general)
	// typically uses.
	frameSize := sampleRate / 50 * channels // samples per 20ms frame
	opusBuf := make([]byte, 4000)           // safe upper bound for one encoded frame

	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	for i := 0; i+frameSize <= len(pcm); i += frameSize {
		frame := pcm[i : i+frameSize]

		n, err := encoder.Encode(frame, opusBuf)
		if err != nil {
			return fmt.Errorf("encoding pcm frame: %w", err)
		}

		if err := p.localTrack.WriteSample(media.Sample{Data: opusBuf[:n], Duration: 20 * time.Millisecond}); err != nil {
			return fmt.Errorf("writing sample: %w", err)
		}

		<-ticker.C // pace to real time, same reasoning as streamAudioFile
	}

	return nil
}

// streamAudioFile reads an Ogg/Opus file and paces it out onto the track
// in real time (Opus frames are typically 20ms).
func (p *Peer) streamAudioFile(filename string) {
	file, err := openAudioFile(filename)
	if err != nil {
		fmt.Println("Peer could not open audio file, skipping playback:", err)
		return
	}
	defer file.Close()

	ogg, _, err := oggreader.NewWith(file)
	if err != nil {
		fmt.Println("Peer could not parse ogg file:", err)
		return
	}

	var lastGranule uint64
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	for ; true; <-ticker.C {
		pageData, pageHeader, err := ogg.ParseNextPage()
		if err == io.EOF {
			fmt.Println("Peer done streaming audio file")
			return
		}
		if err != nil {
			fmt.Println("Peer error reading ogg page:", err)
			return
		}

		sampleCount := float64(pageHeader.GranulePosition - lastGranule)
		lastGranule = pageHeader.GranulePosition
		sampleDuration := time.Duration((sampleCount/48000)*1000) * time.Millisecond

		if err := p.localTrack.WriteSample(media.Sample{Data: pageData, Duration: sampleDuration}); err != nil {
			fmt.Println("Peer error writing sample:", err)
			return
		}
	}
}

func (p *Peer) tryNegotiatePeer() error {
	offer, _ := p.pc.CreateOffer(nil)
	offerErr := trySendOffer(p.pc, &offer)
	if offerErr != nil {
		//fmt.Println("Could not send offer, try send answer instead")
		// send answer instead, then poll for offer
		// poll for offer after sending answer is successful
		if err := p.pollForOffer(&offer); err != nil {
			return fmt.Errorf("Failed to poll for offer")
		}

		p.pc.Close()

		// reset peer connection, this rebinds callback for outgoing/incoming audio handlers
		pc, localTrack, err := p.setupPeerConnection()
		p.pc = pc
		p.localTrack = localTrack
		if err != nil {
			return fmt.Errorf("Failed to rebind localtrack handler")
		}

		pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
			//fmt.Println("Peer ICE state:", state.String())
			if state == webrtc.ICEConnectionStateConnected {
				//go streamAudioFile(localTrack, audioFile)
			}
		})

		//fmt.Println("Peer got offer, setting remote description")
		if err := pc.SetRemoteDescription(offer); err != nil {
			return err
		}

		if err := trySendAnswer(pc); err != nil {
			// should retry here
			return fmt.Errorf("Failed to send answer as peer")
		}
		return nil
	}
	p.isOfferer = true

	//fmt.Println("Sent offer, polling for answer")
	if err := p.pollForAnswer(p.pc); err != nil {
		return fmt.Errorf("Failed to poll for answer")
	}

	return nil
}

func trySendOffer(pc *webrtc.PeerConnection, offer *webrtc.SessionDescription) error {
	gatherComplete := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(*offer); err != nil {
		return err
	}
	<-gatherComplete // waits until ICE candidates are embedded in the SDP

	newSignalPayload := PeerSignalPayload{
		SignalPayload: *pc.LocalDescription(),
		PeerName:      peerName,
	}

	payload, err := json.Marshal(newSignalPayload)

	resp, err := http.Post(signalingBaseURL+"/offer", "application/json", bytes.NewReader(payload))

	if err != nil {
		return fmt.Errorf("posting offer: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		return errors.New("Offer already set")
	}

	return nil
}

func trySendAnswer(pc *webrtc.PeerConnection) error {
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		fmt.Println(err)
		return err
	}

	gatherComplete := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		return err
	}
	<-gatherComplete

	newSignalPayload := PeerSignalPayload{
		SignalPayload: *pc.LocalDescription(),
		PeerName:      peerName,
	}

	payload, err := json.Marshal(newSignalPayload)

	resp, err := http.Post(signalingBaseURL+"/answer", "application/json", bytes.NewReader(payload))

	if err != nil {
		return fmt.Errorf("posting answer: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Failed to post answer")
	}

	return nil
}

// runOfferer creates the offer, posts it to the signaling server, then
// polls for the answer.
func (p *Peer) pollForAnswer(pc *webrtc.PeerConnection) error {
	for {
		resp, err := http.Get(signalingBaseURL + "/answer")
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			var peerSignalPayload PeerSignalPayload
			if err := json.NewDecoder(resp.Body).Decode(&peerSignalPayload); err != nil {
				return err
			}
			p.otherPeer = peerSignalPayload.PeerName
			return pc.SetRemoteDescription(peerSignalPayload.SignalPayload)
		}
		time.Sleep(time.Second)
	}
}

func (p *Peer) pollForOffer(offer *webrtc.SessionDescription) error {
	for {
		resp, err := http.Get(signalingBaseURL + "/offer")
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			var peerSignalPayload PeerSignalPayload
			if err := json.NewDecoder(resp.Body).Decode(&peerSignalPayload); err != nil {
				return err
			}
			p.otherPeer = peerSignalPayload.PeerName
			*offer = peerSignalPayload.SignalPayload

			break
		}
		time.Sleep(time.Second)
	}

	return nil
}
