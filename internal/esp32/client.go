package esp32

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.bug.st/serial"
)

// Client manages the USB CDC serial connection to the ESP32-S3 e-Paper display and button board.
type Client struct {
	cfg       Config
	port      serial.Port
	portName  string
	writeMu   sync.Mutex
	stateMu   sync.RWMutex
	handlers  []ButtonHandler
	onDisc    func()

	isConnected atomic.Bool
	stopChan    chan struct{}
	stopped     atomic.Bool
	wg          sync.WaitGroup
}

// NewClient initializes a new ESP32 serial communication client.
func NewClient(cfg Config) *Client {
	if cfg.BaudRate <= 0 {
		cfg.BaudRate = 115200
	}
	if cfg.ReadTimeout <= 0 {
		cfg.ReadTimeout = 200 * time.Millisecond
	}
	if cfg.ReconnectPeriod <= 0 {
		cfg.ReconnectPeriod = 2 * time.Second
	}

	c := &Client{
		cfg:      cfg,
		stopChan: make(chan struct{}),
	}

	if cfg.OnButton != nil {
		c.handlers = append(c.handlers, cfg.OnButton)
	}

	return c
}

// OnButton registers a callback to receive button press and release events.
func (c *Client) OnButton(handler ButtonHandler) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.handlers = append(c.handlers, handler)
}

// OnDisconnect registers a callback invoked when the serial link drops.
func (c *Client) OnDisconnect(handler func()) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.onDisc = handler
}

// IsConnected reports whether the client has an active serial connection to the ESP32.
func (c *Client) IsConnected() bool {
	return c.isConnected.Load()
}

// PortName returns the name of the currently connected or configured serial port.
func (c *Client) PortName() string {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.portName
}

// Start launches the background connection manager and reader goroutines.
func (c *Client) Start() {
	c.wg.Add(1)
	go c.connectionLoop()
}

// Close gracefully closes the serial port and stops background goroutines.
func (c *Client) Close() {
	if c.stopped.Swap(true) {
		return
	}
	close(c.stopChan)

	c.writeMu.Lock()
	if c.port != nil {
		_ = c.port.Close()
	}
	c.writeMu.Unlock()

	c.wg.Wait()
}

// SendCommand writes a command line to the ESP32 (e.g. "/user Hello", "/agent Hi", "/clear", "/beep").
func (c *Client) SendCommand(cmd string) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	if !c.isConnected.Load() || c.port == nil {
		return errors.New("esp32 device not connected")
	}

	trimmed := strings.TrimRight(cmd, "\r\n")
	msg := trimmed + "\n"

	_, err := c.port.Write([]byte(msg))
	if err != nil {
		return fmt.Errorf("failed to write to esp32 serial: %w", err)
	}

	_ = c.port.Drain()
	return nil
}

// connectionLoop manages automatic connection and reconnection to the ESP32 board.
func (c *Client) connectionLoop() {
	defer c.wg.Done()

	var notFoundLogged bool

	for {
		select {
		case <-c.stopChan:
			return
		default:
		}

		targetPort, err := FindSerialPort(c.cfg.PortName)
		if err != nil {
			if !notFoundLogged {
				log.Printf("[ESP32] Notice: No ESP32 serial device detected (%v). Waiting for hardware connection...", err)
				notFoundLogged = true
			}
			select {
			case <-c.stopChan:
				return
			case <-time.After(c.cfg.ReconnectPeriod):
				continue
			}
		}

		notFoundLogged = false
		mode := &serial.Mode{
			BaudRate: c.cfg.BaudRate,
			DataBits: 8,
			Parity:   serial.NoParity,
			StopBits: serial.OneStopBit,
		}

		port, err := serial.Open(targetPort, mode)
		if err != nil {
			select {
			case <-c.stopChan:
				return
			case <-time.After(c.cfg.ReconnectPeriod):
				continue
			}
		}

		// Configure CDC control lines
		_ = port.SetDTR(true)
		_ = port.SetRTS(true)
		_ = port.SetReadTimeout(c.cfg.ReadTimeout)
		_ = port.ResetInputBuffer()

		c.writeMu.Lock()
		c.port = port
		c.writeMu.Unlock()

		c.stateMu.Lock()
		c.portName = targetPort
		c.stateMu.Unlock()

		c.isConnected.Store(true)
		log.Printf("[ESP32] \033[1;32mConnected\033[0m to hardware on \033[1;36m%s\033[0m (%d baud). Listening for button events.",
			targetPort, c.cfg.BaudRate)

		// Run read loop until port closes or errors out
		c.readLoop(port)

		// Cleanup on disconnect
		c.isConnected.Store(false)
		c.writeMu.Lock()
		if c.port != nil {
			_ = c.port.Close()
			c.port = nil
		}
		c.writeMu.Unlock()

		c.stateMu.RLock()
		discCb := c.onDisc
		c.stateMu.RUnlock()
		if discCb != nil {
			discCb()
		}

		select {
		case <-c.stopChan:
			return
		default:
			log.Printf("[ESP32] Serial port disconnected. Retrying connection...")
			time.Sleep(c.cfg.ReconnectPeriod)
		}
	}
}

// readLoop reads stream bytes, splits on newlines, and parses button event JSON lines.
func (c *Client) readLoop(port serial.Port) {
	buf := make([]byte, 512)
	var lineBuf bytes.Buffer

	for {
		select {
		case <-c.stopChan:
			return
		default:
		}

		n, err := port.Read(buf)
		if err != nil {
			if errors.Is(err, io.EOF) || strings.Contains(err.Error(), "closed") {
				return
			}
			// Transient read timeout is expected when no data is sent within ReadTimeout
			continue
		}

		if n == 0 {
			continue
		}

		for i := 0; i < n; i++ {
			b := buf[i]
			if b == '\n' {
				line := strings.TrimSpace(lineBuf.String())
				lineBuf.Reset()
				if line != "" {
					c.processIncomingLine(line)
				}
			} else if b != '\r' {
				lineBuf.WriteByte(b)
				// Guard against buffer overflow
				if lineBuf.Len() > 2048 {
					lineBuf.Reset()
				}
			}
		}
	}
}

// processIncomingLine parses JSON button events or logs status messages from firmware.
func (c *Client) processIncomingLine(line string) {
	if strings.HasPrefix(line, "{") {
		var evt ButtonEvent
		if err := json.Unmarshal([]byte(line), &evt); err == nil && evt.Event == "button" {
			c.dispatchButton(evt)
			return
		}
	}

	// Non-button responses (e.g. "OK", "BEEP OK", firmware logs)
	if line != "OK" && line != "BEEP OK" {
		log.Printf("[ESP32 Serial] %s", line)
	}
}

// dispatchButton delivers a button event to all registered listeners.
func (c *Client) dispatchButton(evt ButtonEvent) {
	c.stateMu.RLock()
	handlers := append([]ButtonHandler(nil), c.handlers...)
	c.stateMu.RUnlock()

	for _, h := range handlers {
		if h != nil {
			h(evt)
		}
	}
}

// FindSerialPort locates the ESP32 USB CDC serial port on macOS or Linux.
func FindSerialPort(preferred string) (string, error) {
	// 1. Explicitly configured port
	if preferred != "" {
		if _, err := os.Stat(preferred); err == nil {
			return preferred, nil
		}
		return "", fmt.Errorf("configured serial port %q does not exist", preferred)
	}

	// 2. Scan standard device paths (matches test_buttons.py logic)
	// macOS patterns
	macPatterns := []string{
		"/dev/cu.usbmodem*",
		"/dev/cu.usb*",
	}
	for _, pattern := range macPatterns {
		matches, err := filepath.Glob(pattern)
		if err == nil && len(matches) > 0 {
			return matches[0], nil
		}
	}

	// Linux patterns (OpenStick Linux, Debian, Ubuntu)
	linuxPatterns := []string{
		"/dev/ttyACM*",
		"/dev/ttyUSB*",
	}
	for _, pattern := range linuxPatterns {
		matches, err := filepath.Glob(pattern)
		if err == nil && len(matches) > 0 {
			return matches[0], nil
		}
	}

	// 3. Fallback: Query system ports list via serial library
	ports, err := serial.GetPortsList()
	if err == nil {
		for _, p := range ports {
			lower := strings.ToLower(p)
			if strings.Contains(lower, "usbmodem") || strings.Contains(lower, "ttyacm") || strings.Contains(lower, "ttyusb") {
				return p, nil
			}
		}
	}

	return "", errors.New("no ESP32 USB serial device found (/dev/cu.usbmodem* or /dev/ttyACM*)")
}
