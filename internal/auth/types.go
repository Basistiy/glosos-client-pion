package auth

// User represents the authenticated Glosos user information.
type User struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email,omitempty"`
}

// LoginRequest represents the payload for /api/auth/login.
type LoginRequest struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

// LoginResponse represents the response from /api/auth/login.
type LoginResponse struct {
	Message              string `json:"message,omitempty"`
	Token                string `json:"token"`
	RefreshToken         string `json:"refreshToken"`
	RequiresVerification bool   `json:"requiresVerification,omitempty"`
	Error                string `json:"error,omitempty"`
	User                 User   `json:"user"`
}

// RefreshRequest represents the payload for /api/auth/refresh.
type RefreshRequest struct {
	RefreshToken string `json:"refreshToken"`
}

// RefreshResponse represents the response from /api/auth/refresh.
type RefreshResponse struct {
	Token string `json:"token"`
	Error string `json:"error,omitempty"`
}

// TurnCredentialsResponse represents the response from /api/turn/credentials.
type TurnCredentialsResponse struct {
	URL        string   `json:"url"`
	URLs       []string `json:"urls"`
	Username   string   `json:"username"`
	Credential string   `json:"credential"`
	Error      string   `json:"error,omitempty"`
}
