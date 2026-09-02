package audio

import (
	"fmt"
	"sync"

	"layeh.com/gopus"
)

const (
	// SampleRate is the standard WebRTC Opus audio sample rate (48kHz).
	SampleRate = 48000

	// Channels is 1 for mono speech (standard low latency WebRTC voice).
	Channels = 1

	// FrameDurationMs is the standard 20ms WebRTC audio packet duration.
	FrameDurationMs = 20

	// FrameSize is the number of samples per 20ms frame at 48kHz (48000 * 0.02 = 960).
	FrameSize = SampleRate * FrameDurationMs / 1000

	// MaxOpusPacketSize is the maximum expected Opus packet size in bytes.
	MaxOpusPacketSize = 4000
)

// Codec wraps an Opus encoder and decoder using standard libopus bindings.
type Codec struct {
	encMu   sync.Mutex
	encoder *gopus.Encoder

	decMu   sync.Mutex
	decoder *gopus.Decoder
}

// NewCodec creates a new Opus Codec configured for real-time VoIP voice streaming.
func NewCodec() (*Codec, error) {
	enc, err := gopus.NewEncoder(SampleRate, Channels, gopus.Voip)
	if err != nil {
		return nil, fmt.Errorf("failed to create opus encoder: %w", err)
	}

	enc.SetBitrate(32000) // 32kbps VoIP voice

	dec, err := gopus.NewDecoder(SampleRate, Channels)
	if err != nil {
		return nil, fmt.Errorf("failed to create opus decoder: %w", err)
	}

	return &Codec{
		encoder: enc,
		decoder: dec,
	}, nil
}

// Encode encodes raw 16-bit PCM samples into an Opus packet.
// pcm must contain exactly FrameSize samples (960 samples for 20ms at 48kHz).
func (c *Codec) Encode(pcm []int16) ([]byte, error) {
	c.encMu.Lock()
	defer c.encMu.Unlock()

	if len(pcm) < FrameSize {
		padded := make([]int16, FrameSize)
		copy(padded, pcm)
		pcm = padded
	} else if len(pcm) > FrameSize {
		pcm = pcm[:FrameSize]
	}

	data, err := c.encoder.Encode(pcm, FrameSize, MaxOpusPacketSize)
	if err != nil {
		return nil, fmt.Errorf("opus encode error: %w", err)
	}

	return data, nil
}

// Decode decodes an Opus packet into raw 16-bit PCM samples.
func (c *Codec) Decode(opusData []byte) ([]int16, error) {
	c.decMu.Lock()
	defer c.decMu.Unlock()

	pcm, err := c.decoder.Decode(opusData, FrameSize, false)
	if err != nil {
		return nil, fmt.Errorf("opus decode error: %w", err)
	}

	return pcm, nil
}
