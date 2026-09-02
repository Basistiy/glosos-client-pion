package webrtc

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

// MessageHandler is called when a message is received on the DataChannel.
type MessageHandler func(sender, text string, raw []byte)

// DataChannelManager wraps a Pion RTCDataChannel.
type DataChannelManager struct {
	channel    *webrtc.DataChannel
	mu         sync.RWMutex
	isOpen     bool
	targetName string
	onMessage  MessageHandler
	onOpen     func()
	onClose    func()
}

// NewDataChannelManager creates a wrapper around a WebRTC DataChannel.
func NewDataChannelManager(channel *webrtc.DataChannel, targetName string, onMsg MessageHandler, onOpen, onClose func()) *DataChannelManager {
	dcm := &DataChannelManager{
		channel:    channel,
		targetName: targetName,
		onMessage:  onMsg,
		onOpen:     onOpen,
		onClose:    onClose,
	}
	dcm.setupEvents()
	return dcm
}

// SetTargetName updates the displayed remote peer name.
func (d *DataChannelManager) SetTargetName(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.targetName = name
}

// IsOpen returns whether the data channel is in open state.
func (d *DataChannelManager) IsOpen() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.isOpen && d.channel != nil && d.channel.ReadyState() == webrtc.DataChannelStateOpen
}

func (d *DataChannelManager) setupEvents() {
	if d.channel == nil {
		return
	}

	d.channel.OnOpen(func() {
		d.mu.Lock()
		d.isOpen = true
		d.mu.Unlock()

		log.Printf("[WebRTC] DataChannel '%s' (ID %d) opened! Ready to transmit text.", d.channel.Label(), *d.channel.ID())
		if d.onOpen != nil {
			d.onOpen()
		}
	})

	d.channel.OnClose(func() {
		d.mu.Lock()
		d.isOpen = false
		d.mu.Unlock()

		log.Printf("[WebRTC] DataChannel '%s' closed.", d.channel.Label())
		if d.onClose != nil {
			d.onClose()
		}
	})

	d.channel.OnError(func(err error) {
		log.Printf("[WebRTC] DataChannel error: %v", err)
	})

	d.channel.OnMessage(func(msg webrtc.DataChannelMessage) {
		msgStr := string(msg.Data)
		d.mu.RLock()
		peerName := d.targetName
		d.mu.RUnlock()
		if peerName == "" {
			peerName = "Peer"
		}

		// Try parsing JSON structure (e.g. {"type":"transcription","text":"..."} or {"type":"agent","text":"..."})
		var parsed struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}

		if err := json.Unmarshal(msg.Data, &parsed); err == nil && parsed.Text != "" {
			if parsed.Type == "transcription" {
				if d.onMessage != nil {
					d.onMessage("me (transcription)", parsed.Text, msg.Data)
				}
				return
			} else if parsed.Type == "agent" {
				if d.onMessage != nil {
					d.onMessage(fmt.Sprintf("%s (agent)", peerName), parsed.Text, msg.Data)
				}
				return
			}
		}

		if d.onMessage != nil {
			d.onMessage(peerName, msgStr, msg.Data)
		}
	})
}

// SendText sends a plain UTF-8 text string over the DataChannel.
func (d *DataChannelManager) SendText(text string) error {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if d.channel == nil || !d.isOpen || d.channel.ReadyState() != webrtc.DataChannelStateOpen {
		return fmt.Errorf("data channel is not open (current state: %v)", d.readyStateString())
	}

	return d.channel.SendText(text)
}

func (d *DataChannelManager) readyStateString() string {
	if d.channel == nil {
		return "nil"
	}
	return d.channel.ReadyState().String()
}

// Close closes the underlying DataChannel.
func (d *DataChannelManager) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.isOpen = false
	if d.channel != nil {
		return d.channel.Close()
	}
	return nil
}

// DefaultConsoleMessageFormatter formats received chat messages nicely on stdout.
func DefaultConsoleMessageFormatter(sender, text string, raw []byte) {
	now := time.Now().Format("15:04:05")
	fmt.Printf("\n[%s] \033[1;36m%s\033[0m: %s\n> ", now, sender, text)
}
