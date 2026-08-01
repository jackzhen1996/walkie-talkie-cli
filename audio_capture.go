// Listens to the default microphone and prints a live volume meter to
// the terminal. Uses malgo (github.com/gen2brain/malgo), a Go wrapper
// around the miniaudio C library — no separate system audio library
// (like PortAudio) needs to be installed, since miniaudio is bundled
// and compiled via cgo.

package main

import (
	"fmt"
	"github.com/gen2brain/malgo"
	"os"
)

type Recorder struct {
	data       *[]byte
	device     *malgo.Device
	ctx        *malgo.AllocatedContext
	sampleRate uint32
}

func InitRecorder() *Recorder {
	recorder := &Recorder{}

	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(msg string) {
		// malgo internal log messages; ignore or fmt.Print(msg) to debug
		//fmt.Println("[malgo]", msg)
	})

	if err != nil {
		fmt.Fprintln(os.Stderr, "failed to init audio context:", err)
		os.Exit(1)
	}

	deviceConfig := malgo.DefaultDeviceConfig(malgo.Capture)
	deviceConfig.Capture.Format = malgo.FormatS16
	deviceConfig.Capture.Channels = 1
	deviceConfig.SampleRate = 24000
	deviceConfig.Alsa.NoMMap = 1

	recorder.data = &[]byte{}
	onRecvFrames := func(pOutputSample, pInputSamples []byte, framecount uint32) {
		newData := append(*recorder.data, pInputSamples...)
		recorder.data = &newData
	}

	captureCallbacks := malgo.DeviceCallbacks{
		Data: onRecvFrames,
	}

	device, err := malgo.InitDevice(ctx.Context, deviceConfig, captureCallbacks)

	if err != nil {
		fmt.Fprintln(os.Stderr, "failed to init capture device:", err)
		os.Exit(1)
	}
	recorder.device = device
	recorder.ctx = ctx
	recorder.sampleRate = device.SampleRate()

	return recorder
}

// clear this so we don't resend the previous messages
func (r *Recorder) ClearAudioBuffer() {
	r.data = &[]byte{}
}

func (r *Recorder) StartRecording() error {
	//runtime.LockOSThread()
	//defer runtime.UnlockOSThread()
	if err := r.device.Start(); err != nil {
		return err
	}
	return nil
}

func (r *Recorder) StopRecording() {
	r.device.Stop()
	//_ = r.ctx.Uninit()
	//r.ctx.Free()
}

// Run this when the app exits
func (r *Recorder) TearDownRecorder() {
	r.ctx.Uninit()
	r.ctx.Free()
}
