package audio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gen2brain/malgo"
)

// DeviceInfo represents an available audio input/output device.
type DeviceInfo struct {
	ID        string
	Name      string
	IsDefault bool
	Type      string // "capture" or "playback"
}

// DeviceManager handles audio hardware capture (microphone) and playback (speaker).
type DeviceManager struct {
	ctx           *malgo.AllocatedContext
	captureDevice *malgo.Device
	playDevice    *malgo.Device

	inputDeviceName  string
	outputDeviceName string
	useTestTone      bool

	onCapturePCM func([]int16)

	// Playback ring/jitter buffer
	playMu     sync.Mutex
	playBuffer []int16

	// State
	isCapturing atomic.Bool
	isPlaying   atomic.Bool
	stopTone    chan struct{}
}

// NewDeviceManager initializes the audio context.
func NewDeviceManager(inputDeviceName, outputDeviceName string, useTestTone bool) (*DeviceManager, error) {
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize audio context: %w", err)
	}

	return &DeviceManager{
		ctx:              ctx,
		inputDeviceName:  inputDeviceName,
		outputDeviceName: outputDeviceName,
		useTestTone:      useTestTone,
		playBuffer:       make([]int16, 0, SampleRate), // 1 second capacity
	}, nil
}

// ListDevices returns all available capture and playback audio devices.
func (d *DeviceManager) ListDevices() ([]DeviceInfo, error) {
	if d.ctx == nil {
		return nil, errors.New("audio context not initialized")
	}

	var results []DeviceInfo

	// Playback devices
	playbackDevs, err := d.ctx.Devices(malgo.Playback)
	if err == nil {
		for _, dev := range playbackDevs {
			results = append(results, DeviceInfo{
				ID:        dev.ID.String(),
				Name:      dev.Name(),
				IsDefault: dev.IsDefault > 0,
				Type:      "playback",
			})
		}
	}

	// Capture devices
	captureDevs, err := d.ctx.Devices(malgo.Capture)
	if err == nil {
		for _, dev := range captureDevs {
			results = append(results, DeviceInfo{
				ID:        dev.ID.String(),
				Name:      dev.Name(),
				IsDefault: dev.IsDefault > 0,
				Type:      "capture",
			})
		}
	}

	return results, nil
}

// StartCapture begins capturing PCM audio from the microphone (or test tone).
func (d *DeviceManager) StartCapture(onPCM func([]int16)) error {
	if d.isCapturing.Swap(true) {
		return nil // Already running
	}
	d.onCapturePCM = onPCM

	if d.useTestTone {
		log.Println("[Audio] Starting synthetic 440Hz test tone capture generator...")
		d.stopTone = make(chan struct{})
		go d.runTestToneGenerator()
		return nil
	}

	deviceConfig := malgo.DefaultDeviceConfig(malgo.Capture)
	deviceConfig.Capture.Format = malgo.FormatS16
	deviceConfig.Capture.Channels = Channels
	deviceConfig.SampleRate = SampleRate
	deviceConfig.Alsa.NoMMap = 1

	var pcmAccumulator []int16
	var accMu sync.Mutex

	onRecvFrames := func(pOutputSample, pInputSample []byte, framecount uint32) {
		if !d.isCapturing.Load() || len(pInputSample) == 0 {
			return
		}

		samplesCount := len(pInputSample) / 2
		int16Samples := make([]int16, samplesCount)
		for i := 0; i < samplesCount; i++ {
			int16Samples[i] = int16(binary.LittleEndian.Uint16(pInputSample[i*2 : i*2+2]))
		}

		accMu.Lock()
		pcmAccumulator = append(pcmAccumulator, int16Samples...)
		for len(pcmAccumulator) >= FrameSize {
			chunk := make([]int16, FrameSize)
			copy(chunk, pcmAccumulator[:FrameSize])
			pcmAccumulator = pcmAccumulator[FrameSize:]
			accMu.Unlock()

			if d.onCapturePCM != nil {
				d.onCapturePCM(chunk)
			}
			accMu.Lock()
		}
		accMu.Unlock()
	}

	deviceCallbacks := malgo.DeviceCallbacks{
		Data: onRecvFrames,
	}

	device, err := malgo.InitDevice(d.ctx.Context, deviceConfig, deviceCallbacks)
	if err != nil {
		d.isCapturing.Store(false)
		log.Printf("[Audio] Warning: Failed to initialize microphone capture device: %v. Audio sending will be paused.", err)
		return err
	}

	d.captureDevice = device
	if err := device.Start(); err != nil {
		d.isCapturing.Store(false)
		device.Uninit()
		d.captureDevice = nil
		return fmt.Errorf("failed to start capture device: %w", err)
	}

	log.Printf("[Audio] Microphone capture started (48kHz, 16-bit, 20ms frames).")
	return nil
}

// StartPlayback starts the speaker audio output stream.
func (d *DeviceManager) StartPlayback() error {
	if d.isPlaying.Swap(true) {
		return nil // Already running
	}

	deviceConfig := malgo.DefaultDeviceConfig(malgo.Playback)
	deviceConfig.Playback.Format = malgo.FormatS16
	deviceConfig.Playback.Channels = Channels
	deviceConfig.SampleRate = SampleRate
	deviceConfig.Alsa.NoMMap = 1

	onSendFrames := func(pOutputSample, pInputSample []byte, framecount uint32) {
		samplesNeeded := int(framecount) * Channels
		bytesNeeded := samplesNeeded * 2

		d.playMu.Lock()
		available := len(d.playBuffer)
		if available >= samplesNeeded {
			chunk := d.playBuffer[:samplesNeeded]
			d.playBuffer = d.playBuffer[samplesNeeded:]
			d.playMu.Unlock()

			// Write samples to output
			buf := new(bytes.Buffer)
			for _, s := range chunk {
				_ = binary.Write(buf, binary.LittleEndian, s)
			}
			copy(pOutputSample, buf.Bytes())
		} else {
			// Write remaining and pad with silence
			var chunk []int16
			if available > 0 {
				chunk = d.playBuffer
				d.playBuffer = nil
			}
			d.playMu.Unlock()

			buf := new(bytes.Buffer)
			for _, s := range chunk {
				_ = binary.Write(buf, binary.LittleEndian, s)
			}
			outBytes := buf.Bytes()
			copy(pOutputSample, outBytes)
			// Zero out the rest
			for i := len(outBytes); i < bytesNeeded && i < len(pOutputSample); i++ {
				pOutputSample[i] = 0
			}
		}
	}

	deviceCallbacks := malgo.DeviceCallbacks{
		Data: onSendFrames,
	}

	device, err := malgo.InitDevice(d.ctx.Context, deviceConfig, deviceCallbacks)
	if err != nil {
		d.isPlaying.Store(false)
		log.Printf("[Audio] Warning: Failed to initialize speaker playback device: %v. Audio playback will be paused.", err)
		return err
	}

	d.playDevice = device
	if err := device.Start(); err != nil {
		d.isPlaying.Store(false)
		device.Uninit()
		d.playDevice = nil
		return fmt.Errorf("failed to start playback device: %w", err)
	}

	log.Printf("[Audio] Speaker playback started (48kHz, 16-bit, 20ms frames).")
	return nil
}

// PushPlaybackPCM adds decoded PCM samples to the playback queue.
func (d *DeviceManager) PushPlaybackPCM(pcm []int16) {
	d.playMu.Lock()
	defer d.playMu.Unlock()

	// Prevent buffer from growing unbounded (limit to ~500ms jitter buffer: 24,000 samples)
	maxBuffer := SampleRate / 2
	if len(d.playBuffer)+len(pcm) > maxBuffer {
		// Drop oldest samples to prevent latency accumulation
		drop := (len(d.playBuffer) + len(pcm)) - maxBuffer
		if drop < len(d.playBuffer) {
			d.playBuffer = d.playBuffer[drop:]
		} else {
			d.playBuffer = nil
		}
	}

	d.playBuffer = append(d.playBuffer, pcm...)
}

func (d *DeviceManager) runTestToneGenerator() {
	ticker := time.NewTicker(time.Duration(FrameDurationMs) * time.Millisecond)
	defer ticker.Stop()

	phase := 0.0
	frequency := 440.0 // A4 tone
	phaseIncrement := 2.0 * math.Pi * frequency / float64(SampleRate)

	for {
		select {
		case <-d.stopTone:
			return
		case <-ticker.C:
			if !d.isCapturing.Load() {
				continue
			}
			chunk := make([]int16, FrameSize)
			for i := 0; i < FrameSize; i++ {
				chunk[i] = int16(math.Sin(phase) * 8000) // Moderate volume
				phase += phaseIncrement
				if phase >= 2.0*math.Pi {
					phase -= 2.0 * math.Pi
				}
			}
			if d.onCapturePCM != nil {
				d.onCapturePCM(chunk)
			}
		}
	}
}

// StopCapture stops recording.
func (d *DeviceManager) StopCapture() {
	if !d.isCapturing.Swap(false) {
		return
	}
	if d.stopTone != nil {
		close(d.stopTone)
		d.stopTone = nil
	}
	if d.captureDevice != nil {
		d.captureDevice.Uninit()
		d.captureDevice = nil
	}
}

// StopPlayback stops playback.
func (d *DeviceManager) StopPlayback() {
	if !d.isPlaying.Swap(false) {
		return
	}
	if d.playDevice != nil {
		d.playDevice.Uninit()
		d.playDevice = nil
	}
	d.playMu.Lock()
	d.playBuffer = nil
	d.playMu.Unlock()
}

// Close releases all audio devices and context.
func (d *DeviceManager) Close() {
	d.StopCapture()
	d.StopPlayback()
	if d.ctx != nil {
		d.ctx.Uninit()
		d.ctx.Free()
		d.ctx = nil
	}
}
