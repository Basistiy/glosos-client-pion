package webrtc

import (
	"strings"
	"testing"
)

func TestManagerCreateOfferWithAudioAndDataChannel(t *testing.T) {
	callbacks := Callbacks{}
	mgr := NewManager(callbacks, nil, false, true)
	defer mgr.Close()

	offer, err := mgr.CreateOfferForCall("test-socket-123", "test-peer")
	if err != nil {
		t.Fatalf("Failed to create offer: %v", err)
	}

	if offer == nil || offer.SDP == "" {
		t.Fatalf("Offer SDP is empty")
	}

	sdp := offer.SDP

	// Verify DataChannel m-line
	if !strings.Contains(sdp, "m=application") {
		t.Errorf("Expected SDP to contain 'm=application' for DataChannel, got:\n%s", sdp)
	}

	// Verify Audio m-line
	if !strings.Contains(sdp, "m=audio") {
		t.Errorf("Expected SDP to contain 'm=audio' for Opus voice transfer, got:\n%s", sdp)
	}

	// Verify Opus codec is present in SDP
	if !strings.Contains(strings.ToLower(sdp), "opus") {
		t.Errorf("Expected SDP to mention 'opus' codec, got:\n%s", sdp)
	}
}
