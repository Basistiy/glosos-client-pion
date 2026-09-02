package audio

import (
	"math"
	"testing"
)

func TestOpusCodecRoundTrip(t *testing.T) {
	codec, err := NewCodec()
	if err != nil {
		t.Fatalf("Failed to create Opus codec: %v", err)
	}

	// Generate 20ms sine wave at 440 Hz
	pcmInput := make([]int16, FrameSize)
	for i := range pcmInput {
		angle := 2.0 * math.Pi * 440.0 * float64(i) / float64(SampleRate)
		pcmInput[i] = int16(math.Sin(angle) * 16000)
	}

	// Encode
	opusData, err := codec.Encode(pcmInput)
	if err != nil {
		t.Fatalf("Failed to encode PCM to Opus: %v", err)
	}

	if len(opusData) == 0 {
		t.Fatalf("Opus encoded output is empty")
	}

	t.Logf("Encoded %d PCM samples into %d bytes of Opus data", len(pcmInput), len(opusData))

	// Decode
	pcmOutput, err := codec.Decode(opusData)
	if err != nil {
		t.Fatalf("Failed to decode Opus to PCM: %v", err)
	}

	if len(pcmOutput) != FrameSize {
		t.Fatalf("Expected decoded frame size %d, got %d", FrameSize, len(pcmOutput))
	}
}
