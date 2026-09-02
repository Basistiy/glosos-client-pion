package webrtc

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/Basistiy/glosos-client-pion/internal/auth"
	"github.com/pion/webrtc/v4"
)

// SessionDescriptionJSON matches the standard WebRTC RTCSessionDescriptionInit structure.
type SessionDescriptionJSON struct {
	Type string `json:"type"`
	SDP  string `json:"sdp"`
}

// ICECandidateJSON matches the standard WebRTC RTCIceCandidateInit structure.
type ICECandidateJSON struct {
	Candidate        string  `json:"candidate"`
	SDPMid           *string `json:"sdpMid,omitempty"`
	SDPMLineIndex    *uint16 `json:"sdpMLineIndex,omitempty"`
	UsernameFragment *string `json:"usernameFragment,omitempty"`
}

// Callbacks contains event triggers for WebRTC state changes.
type Callbacks struct {
	OnLocalICECandidate func(targetSocketID string, candidate ICECandidateJSON)
	OnConnectionState   func(state webrtc.PeerConnectionState)
	OnDataChannelOpen   func()
	OnDataChannelClose  func()
	OnDataChannelMsg    MessageHandler
}

// Manager manages the lifecycle of a Pion WebRTC PeerConnection.
type Manager struct {
	mu                 sync.RWMutex
	pc                 *webrtc.PeerConnection
	dataChannelMgr     *DataChannelManager
	targetSocketID     string
	targetUsername     string
	callbacks          Callbacks
	turnCreds          *auth.TurnCredentialsResponse
	useTurn            bool
	pendingCandidates  []webrtc.ICECandidateInit
	isRemoteDescSet    bool
	api                *webrtc.API
}

// NewManager creates a new WebRTC Manager instance.
func NewManager(callbacks Callbacks, turnCreds *auth.TurnCredentialsResponse, useTurn bool) *Manager {
	return &Manager{
		callbacks: callbacks,
		turnCreds: turnCreds,
		useTurn:   useTurn,
	}
}

// SetTarget updates target peer metadata.
func (m *Manager) SetTarget(socketID, username string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.targetSocketID = socketID
	m.targetUsername = username
	if m.dataChannelMgr != nil {
		m.dataChannelMgr.SetTargetName(username)
	}
}

// TargetSocketID returns the current active target socket ID.
func (m *Manager) TargetSocketID() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.targetSocketID
}

// TargetUsername returns the current active target peer username.
func (m *Manager) TargetUsername() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.targetUsername
}

// DataChannelManager returns the active DataChannel manager if established.
func (m *Manager) DataChannelManager() *DataChannelManager {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.dataChannelMgr
}

// IsActive returns true if a peer connection is currently active (connecting or connected).
func (m *Manager) IsActive() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.pc == nil {
		return false
	}
	state := m.pc.ConnectionState()
	return state == webrtc.PeerConnectionStateNew ||
		state == webrtc.PeerConnectionStateConnecting ||
		state == webrtc.PeerConnectionStateConnected
}

// IsConnected returns true if DataChannel is open.
func (m *Manager) IsConnected() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.dataChannelMgr != nil && m.dataChannelMgr.IsOpen()
}

// buildRTCConfiguration constructs the Pion RTCConfiguration with STUN and TURN servers.
func (m *Manager) buildRTCConfiguration() webrtc.Configuration {
	iceServers := []webrtc.ICEServer{
		{
			URLs: []string{
				"stun:stun.l.google.com:19302",
				"stun:stun1.l.google.com:19302",
				"stun:stun2.l.google.com:19302",
			},
		},
	}

	if m.useTurn && m.turnCreds != nil {
		turnURLs := m.turnCreds.URLs
		if len(turnURLs) == 0 && m.turnCreds.URL != "" {
			turnURLs = []string{m.turnCreds.URL}
		}

		if len(turnURLs) > 0 {
			log.Printf("[WebRTC] Configuring TURN relay servers: %v", turnURLs)
			iceServers = append(iceServers, webrtc.ICEServer{
				URLs:           turnURLs,
				Username:       m.turnCreds.Username,
				Credential:     m.turnCreds.Credential,
				CredentialType: webrtc.ICECredentialTypePassword,
			})
		}
	}

	return webrtc.Configuration{
		ICEServers: iceServers,
	}
}

// initPeerConnection creates and initializes a new Pion PeerConnection.
func (m *Manager) initPeerConnection(targetSocketID, targetUsername string) error {
	m.Close()

	m.mu.Lock()
	m.targetSocketID = targetSocketID
	m.targetUsername = targetUsername
	m.pendingCandidates = nil
	m.isRemoteDescSet = false

	config := m.buildRTCConfiguration()
	settingEngine := webrtc.SettingEngine{}
	api := webrtc.NewAPI(webrtc.WithSettingEngine(settingEngine))
	m.api = api

	pc, err := api.NewPeerConnection(config)
	if err != nil {
		m.mu.Unlock()
		return fmt.Errorf("failed to create peer connection: %w", err)
	}

	m.pc = pc
	m.mu.Unlock()

	// Handle ICE Candidates
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		candidateJSON := c.ToJSON()
		iceJSON := ICECandidateJSON{
			Candidate:        candidateJSON.Candidate,
			SDPMid:           candidateJSON.SDPMid,
			SDPMLineIndex:    candidateJSON.SDPMLineIndex,
			UsernameFragment: candidateJSON.UsernameFragment,
		}

		m.mu.RLock()
		targetID := m.targetSocketID
		m.mu.RUnlock()

		if m.callbacks.OnLocalICECandidate != nil && targetID != "" {
			m.callbacks.OnLocalICECandidate(targetID, iceJSON)
		}
	})

	// Handle Connection State
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("[WebRTC] PeerConnection state changed: \033[1;33m%s\033[0m", state.String())
		if m.callbacks.OnConnectionState != nil {
			m.callbacks.OnConnectionState(state)
		}

		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			m.Close()
		}
	})

	return nil
}

// CreateOfferForCall initializes a PeerConnection as the initiator, creates the 'chat' DataChannel, and generates an SDP offer.
func (m *Manager) CreateOfferForCall(targetSocketID, targetUsername string) (*SessionDescriptionJSON, error) {
	if err := m.initPeerConnection(targetSocketID, targetUsername); err != nil {
		return nil, err
	}

	m.mu.Lock()
	pc := m.pc

	// Create Data Channel
	log.Printf("[WebRTC] Creating DataChannel 'chat'...")
	channel, err := pc.CreateDataChannel("chat", nil)
	if err != nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("failed to create data channel: %w", err)
	}

	m.dataChannelMgr = NewDataChannelManager(
		channel,
		targetUsername,
		m.callbacks.OnDataChannelMsg,
		m.callbacks.OnDataChannelOpen,
		m.callbacks.OnDataChannelClose,
	)
	m.mu.Unlock()

	// Create Offer
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create offer: %w", err)
	}

	if err := pc.SetLocalDescription(offer); err != nil {
		return nil, fmt.Errorf("failed to set local description: %w", err)
	}

	log.Printf("[WebRTC] Local SDP offer created successfully.")
	return &SessionDescriptionJSON{
		Type: offer.Type.String(),
		SDP:  offer.SDP,
	}, nil
}

// AcceptIncomingCall processes an incoming SDP offer as the callee, sets up the remote DataChannel listener, and generates an SDP answer.
func (m *Manager) AcceptIncomingCall(callerSocketID, callerUsername string, rawOffer interface{}) (*SessionDescriptionJSON, error) {
	if err := m.initPeerConnection(callerSocketID, callerUsername); err != nil {
		return nil, err
	}

	m.mu.Lock()
	pc := m.pc

	// Listen for remote incoming DataChannel
	pc.OnDataChannel(func(d *webrtc.DataChannel) {
		log.Printf("[WebRTC] Incoming remote DataChannel received: '%s' (ID %d)", d.Label(), *d.ID())
		m.mu.Lock()
		m.dataChannelMgr = NewDataChannelManager(
			d,
			callerUsername,
			m.callbacks.OnDataChannelMsg,
			m.callbacks.OnDataChannelOpen,
			m.callbacks.OnDataChannelClose,
		)
		m.mu.Unlock()
	})
	m.mu.Unlock()

	// Parse remote SDP offer
	offerSDP, err := parseSessionDescription(rawOffer)
	if err != nil {
		return nil, fmt.Errorf("failed to parse incoming offer: %w", err)
	}

	pionOffer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  offerSDP.SDP,
	}

	if err := pc.SetRemoteDescription(pionOffer); err != nil {
		return nil, fmt.Errorf("failed to set remote description on incoming offer: %w", err)
	}

	m.mu.Lock()
	m.isRemoteDescSet = true
	m.mu.Unlock()

	m.flushPendingCandidates()

	// Generate Answer
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create answer: %w", err)
	}

	if err := pc.SetLocalDescription(answer); err != nil {
		return nil, fmt.Errorf("failed to set local description for answer: %w", err)
	}

	log.Printf("[WebRTC] Local SDP answer created successfully.")
	return &SessionDescriptionJSON{
		Type: answer.Type.String(),
		SDP:  answer.SDP,
	}, nil
}

// HandleCallAccepted sets the remote description when an answer is received.
func (m *Manager) HandleCallAccepted(answererSocketID string, rawAnswer interface{}) error {
	m.mu.Lock()
	pc := m.pc
	if pc == nil {
		m.mu.Unlock()
		return errors.New("cannot handle answer: peer connection is nil")
	}
	m.mu.Unlock()

	answerSDP, err := parseSessionDescription(rawAnswer)
	if err != nil {
		return fmt.Errorf("failed to parse answer: %w", err)
	}

	pionAnswer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer,
		SDP:  answerSDP.SDP,
	}

	if err := pc.SetRemoteDescription(pionAnswer); err != nil {
		return fmt.Errorf("failed to set remote description for answer: %w", err)
	}

	m.mu.Lock()
	m.isRemoteDescSet = true
	m.mu.Unlock()

	m.flushPendingCandidates()
	log.Printf("[WebRTC] Remote description set from answer.")
	return nil
}

// AddRemoteICECandidate adds a remote ICE candidate, or queues it if the remote description has not been set yet.
func (m *Manager) AddRemoteICECandidate(rawCandidate interface{}) error {
	candInit, err := parseICECandidate(rawCandidate)
	if err != nil {
		return fmt.Errorf("failed to parse remote ice candidate: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.pc != nil && m.isRemoteDescSet {
		return m.pc.AddICECandidate(candInit)
	}

	m.pendingCandidates = append(m.pendingCandidates, candInit)
	return nil
}

func (m *Manager) flushPendingCandidates() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.pc == nil || !m.isRemoteDescSet {
		return
	}

	for _, cand := range m.pendingCandidates {
		if err := m.pc.AddICECandidate(cand); err != nil {
			log.Printf("[WebRTC] Warning: failed to add pending candidate: %v", err)
		}
	}
	m.pendingCandidates = nil
}

// SendTextMessage sends a message through the open data channel.
func (m *Manager) SendTextMessage(text string) error {
	m.mu.RLock()
	dcm := m.dataChannelMgr
	m.mu.RUnlock()

	if dcm == nil {
		return errors.New("no active data channel")
	}
	return dcm.SendText(text)
}

// Close tears down the PeerConnection and DataChannel.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.dataChannelMgr != nil {
		_ = m.dataChannelMgr.Close()
		m.dataChannelMgr = nil
	}

	if m.pc != nil {
		_ = m.pc.Close()
		m.pc = nil
	}

	m.targetSocketID = ""
	m.targetUsername = ""
	m.pendingCandidates = nil
	m.isRemoteDescSet = false
}

// parseSessionDescription converts arbitrary interface{} into SessionDescriptionJSON.
func parseSessionDescription(raw interface{}) (*SessionDescriptionJSON, error) {
	bytes, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var sdp SessionDescriptionJSON
	if err := json.Unmarshal(bytes, &sdp); err != nil {
		return nil, err
	}
	if sdp.SDP == "" {
		return nil, errors.New("session description sdp string is empty")
	}
	return &sdp, nil
}

// parseICECandidate converts arbitrary interface{} into webrtc.ICECandidateInit.
func parseICECandidate(raw interface{}) (webrtc.ICECandidateInit, error) {
	bytes, err := json.Marshal(raw)
	if err != nil {
		return webrtc.ICECandidateInit{}, err
	}
	var cand ICECandidateJSON
	if err := json.Unmarshal(bytes, &cand); err != nil {
		return webrtc.ICECandidateInit{}, err
	}
	return webrtc.ICECandidateInit{
		Candidate:        cand.Candidate,
		SDPMid:           cand.SDPMid,
		SDPMLineIndex:    cand.SDPMLineIndex,
		UsernameFragment: cand.UsernameFragment,
	}, nil
}
