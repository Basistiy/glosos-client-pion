package signaling

// OnlineUser represents a connected user/device session broadcasted by the signaling server.
type OnlineUser struct {
	SocketID    string `json:"socketId"`
	Username    string `json:"username"`
	ClientType  string `json:"clientType,omitempty"`
	DeviceModel string `json:"deviceModel,omitempty"`
}

// CallUserPayload is sent to the server to initiate a call.
type CallUserPayload struct {
	TargetSocketID string      `json:"targetSocketId"`
	Offer          interface{} `json:"offer"`
	UseTurn        bool        `json:"useTurn,omitempty"`
}

// IncomingCallPayload is received from the server when another peer calls us.
type IncomingCallPayload struct {
	CallerSocketID string      `json:"callerSocketId"`
	CallerUsername string      `json:"callerUsername"`
	Offer          interface{} `json:"offer"`
	UseTurn        bool        `json:"useTurn,omitempty"`
}

// MakeAnswerPayload is sent to the server in response to an incoming call.
type MakeAnswerPayload struct {
	TargetSocketID string      `json:"targetSocketId"`
	Answer         interface{} `json:"answer"`
}

// CallAcceptedPayload is received when the callee peer has accepted our call.
type CallAcceptedPayload struct {
	AnswererSocketID string      `json:"answererSocketId"`
	AnswererUsername string      `json:"answererUsername"`
	Answer           interface{} `json:"answer"`
}

// IceCandidatePayload represents an ICE candidate to exchange with the remote peer.
type IceCandidatePayload struct {
	TargetSocketID string      `json:"targetSocketId,omitempty"`
	SenderSocketID string      `json:"senderSocketId,omitempty"`
	Candidate      interface{} `json:"candidate"`
}

// HangUpPayload is sent or received to terminate a call session.
type HangUpPayload struct {
	TargetSocketID string `json:"targetSocketId,omitempty"`
	SenderSocketID string `json:"senderSocketId,omitempty"`
}

// CallFailedPayload is received when a call could not be delivered to a peer.
type CallFailedPayload struct {
	TargetSocketID string `json:"targetSocketId"`
	Reason         string `json:"reason"`
}
