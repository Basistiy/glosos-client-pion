package audio

import (
	"fmt"
	"log"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// Pipeline coordinates full-duplex voice capture, Opus encoding, Opus decoding, and audio playback.
type Pipeline struct {
	codec     *Codec
	devMgr    *DeviceManager
	onOpusOut func([]byte)

	isMuted    atomic.Bool
	isDeafened atomic.Bool
	isRunning  atomic.Bool

	mu sync.RWMutex
}

// Config holds configuration for the audio pipeline.
type Config struct {
	Enabled          bool
	InputDeviceName  string
	OutputDeviceName string
	UseTestTone      bool
	OnLocalOpusFrame func([]byte)
}

// NewPipeline creates a new full-duplex audio pipeline.
func NewPipeline(cfg Config) (*Pipeline, error) {
	codec, err := NewCodec()
	if err != nil {
		return nil, fmt.Errorf("failed to init opus codec: %w", err)
	}

	devMgr, err := NewDeviceManager(cfg.InputDeviceName, cfg.OutputDeviceName, cfg.UseTestTone)
	if err != nil {
		return nil, fmt.Errorf("failed to init audio device manager: %w", err)
	}

	p := &Pipeline{
		codec:     codec,
		devMgr:    devMgr,
		onOpusOut: cfg.OnLocalOpusFrame,
	}

	return p, nil
}

// Start launches capture and playback streams.
func (p *Pipeline) Start() error {
	if p.isRunning.Swap(true) {
		return nil
	}

	// 1. Start Playback
	if err := p.devMgr.StartPlayback(); err != nil {
		log.Printf("[Audio Pipeline] Notice: Audio playback initialization failed (%v). Continuing without speaker.", err)
	}

	// 2. Start Capture
	err := p.devMgr.StartCapture(func(pcm []int16) {
		if !p.isRunning.Load() {
			return
		}

		if p.isMuted.Load() {
			// Muted: do not transmit voice
			return
		}

		opusData, err := p.codec.Encode(pcm)
		if err != nil {
			log.Printf("[Audio Pipeline] Encode error: %v", err)
			return
		}

		if len(opusData) > 0 {
			p.mu.RLock()
			cb := p.onOpusOut
			p.mu.RUnlock()

			if cb != nil {
				cb(opusData)
			}
		}
	})

	if err != nil {
		log.Printf("[Audio Pipeline] Notice: Audio capture initialization failed (%v). Continuing without microphone.", err)
	}

	return nil
}

// SetLocalOpusHandler updates the outbound Opus frame handler.
func (p *Pipeline) SetLocalOpusHandler(cb func([]byte)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onOpusOut = cb
}

// PushRemoteOpusFrame decodes an incoming Opus packet and feeds it to the playback buffer.
func (p *Pipeline) PushRemoteOpusFrame(opusData []byte) {
	if !p.isRunning.Load() || p.isDeafened.Load() || len(opusData) == 0 {
		return
	}

	pcm, err := p.codec.Decode(opusData)
	if err != nil {
		// Non-fatal, packet loss / corrupt packet
		return
	}

	if len(pcm) > 0 {
		p.devMgr.PushPlaybackPCM(pcm)
	}
}

// SetMuted toggles microphone audio transmission.
func (p *Pipeline) SetMuted(muted bool) {
	p.isMuted.Store(muted)
}

// IsMuted returns true if microphone sending is muted.
func (p *Pipeline) IsMuted() bool {
	return p.isMuted.Load()
}

// SetDeafened toggles incoming speaker audio playback.
func (p *Pipeline) SetDeafened(deafened bool) {
	p.isDeafened.Store(deafened)
}

// IsDeafened returns true if incoming audio playback is disabled.
func (p *Pipeline) IsDeafened() bool {
	return p.isDeafened.Load()
}

// ListDevices returns all available audio devices.
func (p *Pipeline) ListDevices() ([]DeviceInfo, error) {
	if p.devMgr == nil {
		return nil, fmt.Errorf("device manager not initialized")
	}
	return p.devMgr.ListDevices()
}

// Close stops and frees the audio pipeline.
func (p *Pipeline) Close() {
	if !p.isRunning.Swap(false) {
		return
	}
	if p.devMgr != nil {
		p.devMgr.Close()
	}
}

// PlayConnectSound plays a cheerful ascending two-tone chime (587Hz D5 -> 880Hz A5) through the speaker
// indicating that the WebRTC peer connection has been established.
func (p *Pipeline) PlayConnectSound() {
	if !p.isRunning.Load() || p.devMgr == nil {
		return
	}
	var pcm []int16
	pcm = append(pcm, generateTonePCM(587.33, 75*time.Millisecond, 0.35)...)
	pcm = append(pcm, generateTonePCM(0, 20*time.Millisecond, 0)...) // brief pause
	pcm = append(pcm, generateTonePCM(880.00, 140*time.Millisecond, 0.35)...)
	p.devMgr.PushPlaybackPCM(pcm)
}

// PlayServerReadySound plays a subtle single confirmation tone (784Hz G5, 60ms)
// indicating that the client has successfully authenticated with the signaling server.
func (p *Pipeline) PlayServerReadySound() {
	if !p.isRunning.Load() || p.devMgr == nil {
		return
	}
	pcm := generateTonePCM(783.99, 60*time.Millisecond, 0.25)
	p.devMgr.PushPlaybackPCM(pcm)
}

// PlayPTTStartSound plays a short, crisp audio feedback beep (988Hz B5, 60ms)
// through the speaker when Push-To-Talk (PTT) recording button is pressed.
func (p *Pipeline) PlayPTTStartSound() {
	if !p.isRunning.Load() || p.devMgr == nil {
		return
	}
	pcm := generateTonePCM(987.77, 60*time.Millisecond, 0.30)
	p.devMgr.PushPlaybackPCM(pcm)
}

// PlayDisconnectSound plays a gentle descending two-tone cue (784Hz G5 -> 523Hz C5)
// indicating that the WebRTC session ended.
func (p *Pipeline) PlayDisconnectSound() {
	if !p.isRunning.Load() || p.devMgr == nil {
		return
	}
	var pcm []int16
	pcm = append(pcm, generateTonePCM(783.99, 70*time.Millisecond, 0.30)...)
	pcm = append(pcm, generateTonePCM(0, 20*time.Millisecond, 0)...) // brief pause
	pcm = append(pcm, generateTonePCM(523.25, 110*time.Millisecond, 0.30)...)
	p.devMgr.PushPlaybackPCM(pcm)
}

// generateTonePCM synthesizes 16-bit 48kHz mono PCM for a given frequency, duration, and volume (0.0 - 1.0)
// with attack and decay ramps to prevent clicks.
func generateTonePCM(freq float64, duration time.Duration, volume float64) []int16 {
	sampleCount := int(float64(SampleRate) * duration.Seconds())
	if sampleCount <= 0 {
		return nil
	}
	pcm := make([]int16, sampleCount)
	if freq <= 0 {
		return pcm // silence
	}

	maxAmp := 32767.0 * volume
	if maxAmp > 32767.0 {
		maxAmp = 32767.0
	}

	fadeSamples := int(float64(SampleRate) * 0.008) // 8ms fade
	if fadeSamples*2 > sampleCount {
		fadeSamples = sampleCount / 2
	}

	for i := 0; i < sampleCount; i++ {
		env := 1.0
		if i < fadeSamples {
			env = float64(i) / float64(fadeSamples)
		} else if i > sampleCount-fadeSamples {
			env = float64(sampleCount-i) / float64(fadeSamples)
		}
		angle := 2.0 * math.Pi * freq * float64(i) / float64(SampleRate)
		pcm[i] = int16(math.Sin(angle) * maxAmp * env)
	}
	return pcm
}

