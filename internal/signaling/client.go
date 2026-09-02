package signaling

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Handlers holds callback functions for signaling events.
type Handlers struct {
	OnConnected    func(socketID string)
	OnDisconnected func(err error)
	OnOnlineUsers  func(users []OnlineUser)
	OnIncomingCall func(payload IncomingCallPayload)
	OnCallAccepted func(payload CallAcceptedPayload)
	OnIceCandidate func(payload IceCandidatePayload)
	OnHangUp       func(payload HangUpPayload)
	OnCallFailed   func(payload CallFailedPayload)
}

// Client manages the Socket.IO signaling connection over WebSocket.
type Client struct {
	serverURL   string
	token       string
	clientType  string
	deviceModel string
	socketID    string
	conn        *websocket.Conn
	writeMu     sync.Mutex
	stateMu     sync.RWMutex
	handlers    Handlers
	isClosed    bool
	stopChan    chan struct{}
}

// NewClient creates a new signaling Client.
func NewClient(serverURL, token, clientType, deviceModel string, handlers Handlers) *Client {
	if clientType == "" {
		clientType = "pion-cli"
	}
	if deviceModel == "" {
		deviceModel = "Go/Pion Client"
	}
	return &Client{
		serverURL:   serverURL,
		token:       token,
		clientType:  clientType,
		deviceModel: deviceModel,
		handlers:    handlers,
		stopChan:    make(chan struct{}),
	}
}

// SocketID returns the assigned socket ID.
func (c *Client) SocketID() string {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.socketID
}

// UpdateToken updates the active token for future reconnects.
func (c *Client) UpdateToken(newToken string) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.token = newToken
}

// Connect establishes the WebSocket connection and completes Socket.IO v4 authentication handshake.
func (c *Client) Connect() error {
	u, err := url.Parse(c.serverURL)
	if err != nil {
		return fmt.Errorf("invalid signaling server url: %w", err)
	}

	wsScheme := "ws"
	if u.Scheme == "https" || u.Scheme == "wss" {
		wsScheme = "wss"
	}

	// Socket.IO v4 path
	wsURL := fmt.Sprintf("%s://%s/socket.io/?EIO=4&transport=websocket", wsScheme, u.Host)
	log.Printf("[Signaling] Connecting to %s ...", wsURL)

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	conn, resp, err := dialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("websocket dial failed with status %d: %w", resp.StatusCode, err)
		}
		return fmt.Errorf("websocket dial failed: %w", err)
	}

	c.conn = conn

	// 1. Read Engine.IO Open Packet (starts with '0')
	msgType, msgBytes, err := conn.ReadMessage()
	if err != nil {
		conn.Close()
		return fmt.Errorf("failed to read Engine.IO open packet: %w", err)
	}
	if msgType != websocket.TextMessage || len(msgBytes) == 0 || msgBytes[0] != '0' {
		conn.Close()
		return fmt.Errorf("unexpected Engine.IO open packet: %s", string(msgBytes))
	}

	log.Printf("[Signaling] Engine.IO handshake established: %s", string(msgBytes))

	// 2. Send Socket.IO Connect Packet: 40{"token":"...","clientType":"...","deviceModel":"..."}
	c.stateMu.RLock()
	currentToken := c.token
	c.stateMu.RUnlock()

	authPayload, err := json.Marshal(map[string]interface{}{
		"token":       currentToken,
		"clientType":  c.clientType,
		"deviceModel": c.deviceModel,
	})
	if err != nil {
		conn.Close()
		return fmt.Errorf("failed to marshal auth payload: %w", err)
	}

	connectMsg := fmt.Sprintf("40%s", string(authPayload))
	c.writeMu.Lock()
	err = conn.WriteMessage(websocket.TextMessage, []byte(connectMsg))
	c.writeMu.Unlock()
	if err != nil {
		conn.Close()
		return fmt.Errorf("failed to send Socket.IO connect packet: %w", err)
	}

	// 3. Read Socket.IO Connect ACK or CONNECT_ERROR
	msgType, msgBytes, err = conn.ReadMessage()
	if err != nil {
		conn.Close()
		return fmt.Errorf("failed to read Socket.IO connect response: %w", err)
	}

	msgStr := string(msgBytes)
	if strings.HasPrefix(msgStr, "44") {
		conn.Close()
		return fmt.Errorf("Socket.IO authentication rejected: %s", msgStr)
	}

	if !strings.HasPrefix(msgStr, "40") {
		conn.Close()
		return fmt.Errorf("unexpected Socket.IO connect response: %s", msgStr)
	}

	// Extract socket ID if returned in connect packet: 40{"sid":"..."}
	var connectResp struct {
		Sid string `json:"sid"`
	}
	rawJSON := strings.TrimPrefix(msgStr, "40")
	if len(rawJSON) > 0 {
		_ = json.Unmarshal([]byte(rawJSON), &connectResp)
	}

	c.stateMu.Lock()
	c.socketID = connectResp.Sid
	c.stateMu.Unlock()

	log.Printf("[Signaling] Socket.IO authenticated successfully! Assigned Socket ID: %s", c.socketID)

	if c.handlers.OnConnected != nil {
		c.handlers.OnConnected(c.socketID)
	}

	// Start reading loop
	go c.readLoop()

	return nil
}

// readLoop listens for incoming Socket.IO / Engine.IO frames.
func (c *Client) readLoop() {
	defer func() {
		c.Close()
		if c.handlers.OnDisconnected != nil {
			c.handlers.OnDisconnected(nil)
		}
	}()

	for {
		msgType, msgBytes, err := c.conn.ReadMessage()
		if err != nil {
			c.stateMu.RLock()
			closed := c.isClosed
			c.stateMu.RUnlock()
			if !closed {
				log.Printf("[Signaling] Connection read error: %v", err)
			}
			return
		}

		if msgType != websocket.TextMessage || len(msgBytes) == 0 {
			continue
		}

		msgStr := string(msgBytes)

		// Engine.IO PING ('2') -> reply with PONG ('3')
		if msgStr == "2" {
			c.writeMu.Lock()
			_ = c.conn.WriteMessage(websocket.TextMessage, []byte("3"))
			c.writeMu.Unlock()
			continue
		}

		// Engine.IO PONG ('3')
		if msgStr == "3" {
			continue
		}

		// Socket.IO EVENT starts with '42'
		if strings.HasPrefix(msgStr, "42") {
			eventPayload := strings.TrimPrefix(msgStr, "42")
			c.handleSocketIOEvent(eventPayload)
			continue
		}

		// Socket.IO DISCONNECT ('41')
		if msgStr == "41" {
			log.Printf("[Signaling] Server requested Socket.IO disconnect.")
			return
		}
	}
}

// handleSocketIOEvent parses ["event-name", data] payload and dispatches to registered handlers.
func (c *Client) handleSocketIOEvent(rawJSON string) {
	var rawArray []json.RawMessage
	if err := json.Unmarshal([]byte(rawJSON), &rawArray); err != nil || len(rawArray) == 0 {
		log.Printf("[Signaling] Failed to parse event array: %s", rawJSON)
		return
	}

	var eventName string
	if err := json.Unmarshal(rawArray[0], &eventName); err != nil {
		log.Printf("[Signaling] Failed to parse event name: %v", err)
		return
	}

	var dataMsg json.RawMessage
	if len(rawArray) > 1 {
		dataMsg = rawArray[1]
	}

	switch eventName {
	case "online-users":
		if c.handlers.OnOnlineUsers != nil {
			var users []OnlineUser
			if err := json.Unmarshal(dataMsg, &users); err == nil {
				c.handlers.OnOnlineUsers(users)
			}
		}

	case "incoming-call":
		if c.handlers.OnIncomingCall != nil {
			var payload IncomingCallPayload
			if err := json.Unmarshal(dataMsg, &payload); err == nil {
				c.handlers.OnIncomingCall(payload)
			}
		}

	case "call-accepted":
		if c.handlers.OnCallAccepted != nil {
			var payload CallAcceptedPayload
			if err := json.Unmarshal(dataMsg, &payload); err == nil {
				c.handlers.OnCallAccepted(payload)
			}
		}

	case "ice-candidate":
		if c.handlers.OnIceCandidate != nil {
			var payload IceCandidatePayload
			if err := json.Unmarshal(dataMsg, &payload); err == nil {
				c.handlers.OnIceCandidate(payload)
			}
		}

	case "hang-up":
		if c.handlers.OnHangUp != nil {
			var payload HangUpPayload
			if err := json.Unmarshal(dataMsg, &payload); err == nil {
				c.handlers.OnHangUp(payload)
			}
		}

	case "call-failed":
		if c.handlers.OnCallFailed != nil {
			var payload CallFailedPayload
			if err := json.Unmarshal(dataMsg, &payload); err == nil {
				c.handlers.OnCallFailed(payload)
			}
		}

	default:
		log.Printf("[Signaling] Received unhandled event: %s", eventName)
	}
}

// Emit sends a Socket.IO event with payload.
func (c *Client) Emit(eventName string, payload interface{}) error {
	c.stateMu.RLock()
	if c.conn == nil || c.isClosed {
		c.stateMu.RUnlock()
		return errors.New("signaling connection is not open")
	}
	c.stateMu.RUnlock()

	msgArray := []interface{}{eventName, payload}
	data, err := json.Marshal(msgArray)
	if err != nil {
		return fmt.Errorf("failed to marshal emit payload: %w", err)
	}

	packet := fmt.Sprintf("42%s", string(data))

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.WriteMessage(websocket.TextMessage, []byte(packet))
}

// CallUser sends a WebRTC offer to the target socket.
func (c *Client) CallUser(targetSocketID string, offer interface{}, useTurn bool) error {
	return c.Emit("call-user", CallUserPayload{
		TargetSocketID: targetSocketID,
		Offer:          offer,
		UseTurn:        useTurn,
	})
}

// MakeAnswer sends a WebRTC answer back to the caller socket.
func (c *Client) MakeAnswer(targetSocketID string, answer interface{}) error {
	return c.Emit("make-answer", MakeAnswerPayload{
		TargetSocketID: targetSocketID,
		Answer:         answer,
	})
}

// SendIceCandidate sends a local ICE candidate to the target peer.
func (c *Client) SendIceCandidate(targetSocketID string, candidate interface{}) error {
	return c.Emit("ice-candidate", IceCandidatePayload{
		TargetSocketID: targetSocketID,
		Candidate:      candidate,
	})
}

// HangUp notifies the target peer that the call is ending.
func (c *Client) HangUp(targetSocketID string) error {
	return c.Emit("hang-up", HangUpPayload{
		TargetSocketID: targetSocketID,
	})
}

// Close closes the WebSocket connection.
func (c *Client) Close() error {
	c.stateMu.Lock()
	if c.isClosed {
		c.stateMu.Unlock()
		return nil
	}
	c.isClosed = true
	conn := c.conn
	c.stateMu.Unlock()

	if conn != nil {
		c.writeMu.Lock()
		_ = conn.WriteMessage(websocket.TextMessage, []byte("41"))
		c.writeMu.Unlock()
		return conn.Close()
	}
	return nil
}
