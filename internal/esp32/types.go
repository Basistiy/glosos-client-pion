package esp32

import "time"

// ButtonEvent represents a button event received from the ESP32 firmware over USB CDC Serial.
// E.g.: {"event":"button","id":"wheel_click","state":"press"}
//       {"event":"button","id":"wheel_click","state":"release","duration_ms":350}
type ButtonEvent struct {
	Event      string `json:"event"`
	ID         string `json:"id"`
	State      string `json:"state"` // "press" or "release"
	DurationMs uint32 `json:"duration_ms,omitempty"`
}

// ButtonHandler is a callback function invoked when a button event is received.
type ButtonHandler func(evt ButtonEvent)

// Config specifies options for the ESP32 serial communication client.
type Config struct {
	PortName        string        // Specific serial port path, or empty for auto-detection
	BaudRate        int           // Default: 115200
	ReadTimeout     time.Duration // Read poll interval / timeout (default: 200ms)
	ReconnectPeriod time.Duration // Interval to retry connecting if device is disconnected (default: 2s)
	OnButton        ButtonHandler // Global button handler
}
