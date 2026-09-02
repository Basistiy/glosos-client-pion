package audio

import (
	"fmt"
	"log"
	"sync"
	"sync/atomic"
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
