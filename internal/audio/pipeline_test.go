package audio

import (
	"testing"
	"time"
)

func TestToneGeneration(t *testing.T) {
	tone := generateTonePCM(440.0, 100*time.Millisecond, 0.5)
	expectedSamples := int(float64(SampleRate) * 0.1)
	if len(tone) != expectedSamples {
		t.Fatalf("Expected %d samples, got %d", expectedSamples, len(tone))
	}

	for _, s := range tone {
		if s > 17000 || s < -17000 {
			t.Fatalf("Sample amplitude %d exceeded expected bound", s)
		}
	}

	silence := generateTonePCM(0, 20*time.Millisecond, 0)
	if len(silence) != int(float64(SampleRate)*0.02) {
		t.Fatalf("Unexpected silence length: %d", len(silence))
	}
	for _, s := range silence {
		if s != 0 {
			t.Fatalf("Expected silence (0), got %d", s)
		}
	}
}
