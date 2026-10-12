package esp32

import (
	"sync"
	"testing"
)

func TestProcessIncomingLine_ButtonEvent(t *testing.T) {
	client := NewClient(Config{})

	var received []ButtonEvent
	var mu sync.Mutex

	client.OnButton(func(evt ButtonEvent) {
		mu.Lock()
		defer mu.Unlock()
		received = append(received, evt)
	})

	// Test 1: Button Press Event
	pressJSON := `{"event":"button","id":"wheel_click","state":"press"}`
	client.processIncomingLine(pressJSON)

	// Test 2: Button Release Event
	releaseJSON := `{"event":"button","id":"wheel_click","state":"release","duration_ms":420}`
	client.processIncomingLine(releaseJSON)

	// Test 3: Unrelated raw text / OK
	client.processIncomingLine("OK")
	client.processIncomingLine("BEEP OK")
	client.processIncomingLine(`{"event":"status","ready":true}`)

	mu.Lock()
	defer mu.Unlock()

	if len(received) != 2 {
		t.Fatalf("expected 2 button events, got %d", len(received))
	}

	if received[0].ID != "wheel_click" || received[0].State != "press" {
		t.Errorf("unexpected first event: %+v", received[0])
	}

	if received[1].ID != "wheel_click" || received[1].State != "release" || received[1].DurationMs != 420 {
		t.Errorf("unexpected second event: %+v", received[1])
	}
}

func TestFindSerialPort_Preferred(t *testing.T) {
	// Non-existent preferred port should return error
	_, err := FindSerialPort("/dev/non_existent_port_12345")
	if err == nil {
		t.Errorf("expected error for nonexistent port, got nil")
	}
}
