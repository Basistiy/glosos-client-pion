package auth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// jwtPayload defines standard JWT claims we inspect.
type jwtPayload struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	Exp      int64  `json:"exp"`
	Iat      int64  `json:"iat"`
}

// ParseJWTExpiration extracts the expiration timestamp from a JWT string.
func ParseJWTExpiration(tokenStr string) (time.Time, error) {
	parts := strings.Split(tokenStr, ".")
	if len(parts) != 3 {
		return time.Time{}, errors.New("invalid jwt format: expected 3 dot-separated parts")
	}

	payloadSegment := parts[1]
	// Handle unpadded base64 URL encoding
	if l := len(payloadSegment) % 4; l > 0 {
		payloadSegment += strings.Repeat("=", 4-l)
	}

	decodedBytes, err := base64.URLEncoding.DecodeString(payloadSegment)
	if err != nil {
		// Fallback to standard Base64 decoding if URL decoding fails
		decodedBytes, err = base64.StdEncoding.DecodeString(payloadSegment)
		if err != nil {
			return time.Time{}, fmt.Errorf("failed to decode jwt payload base64: %w", err)
		}
	}

	var payload jwtPayload
	if err := json.Unmarshal(decodedBytes, &payload); err != nil {
		return time.Time{}, fmt.Errorf("failed to parse jwt json payload: %w", err)
	}

	if payload.Exp == 0 {
		return time.Time{}, errors.New("jwt payload does not contain an 'exp' claim")
	}

	return time.Unix(payload.Exp, 0), nil
}

// TokenRefresher is an interface for refreshing tokens via HTTP.
type TokenRefresher interface {
	RefreshToken(serverURL, refreshToken string) (string, error)
}

// TokenManager manages token state, parses expiration, and performs proactive silent token refreshes in the background.
type TokenManager struct {
	mu           sync.RWMutex
	serverURL    string
	token        string
	refreshToken string
	user         User
	refresher    TokenRefresher
	listeners    []func(newToken string)
	stopChan     chan struct{}
	doneChan     chan struct{}
	isStarted    bool
}

// NewTokenManager creates a new TokenManager instance.
func NewTokenManager(refresher TokenRefresher, serverURL, initialToken, initialRefreshToken string, user User) *TokenManager {
	return &TokenManager{
		serverURL:    serverURL,
		token:        initialToken,
		refreshToken: initialRefreshToken,
		user:         user,
		refresher:    refresher,
		listeners:    make([]func(newToken string), 0),
		stopChan:     make(chan struct{}),
		doneChan:     make(chan struct{}),
	}
}

// GetToken returns the current access token in a thread-safe manner.
func (tm *TokenManager) GetToken() string {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return tm.token
}

// GetRefreshToken returns the refresh token in a thread-safe manner.
func (tm *TokenManager) GetRefreshToken() string {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return tm.refreshToken
}

// GetUser returns the authenticated user info.
func (tm *TokenManager) GetUser() User {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return tm.user
}

// OnTokenRefreshed registers a callback listener called whenever the token is refreshed.
func (tm *TokenManager) OnTokenRefreshed(listener func(newToken string)) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.listeners = append(tm.listeners, listener)
}

func (tm *TokenManager) updateToken(newToken string) {
	tm.mu.Lock()
	tm.token = newToken
	listeners := make([]func(string), len(tm.listeners))
	copy(listeners, tm.listeners)
	tm.mu.Unlock()

	for _, l := range listeners {
		if l != nil {
			go l(newToken)
		}
	}
}

// Start initiates the silent token refresh background goroutine.
func (tm *TokenManager) Start() {
	tm.mu.Lock()
	if tm.isStarted {
		tm.mu.Unlock()
		return
	}
	tm.isStarted = true
	tm.stopChan = make(chan struct{})
	tm.doneChan = make(chan struct{})
	tm.mu.Unlock()

	go tm.refreshLoop()
}

// Stop terminates the background refresh loop gracefully.
func (tm *TokenManager) Stop() {
	tm.mu.Lock()
	if !tm.isStarted {
		tm.mu.Unlock()
		return
	}
	tm.isStarted = false
	close(tm.stopChan)
	tm.mu.Unlock()

	<-tm.doneChan
}

// refreshLoop periodically checks the token expiration and silently requests a new token 1 minute before expiry.
func (tm *TokenManager) refreshLoop() {
	defer close(tm.doneChan)

	log.Printf("[Auth] Starting silent token refresh manager...")

	for {
		currentToken := tm.GetToken()
		rToken := tm.GetRefreshToken()
		if rToken == "" {
			log.Printf("[Auth] No refresh token available. Silent token refresh disabled.")
			return
		}

		expTime, err := ParseJWTExpiration(currentToken)
		var waitDuration time.Duration

		if err != nil {
			log.Printf("[Auth] Warning: Could not parse token expiry (%v). Checking again in 5 minutes.", err)
			waitDuration = 5 * time.Minute
		} else {
			remaining := time.Until(expTime)
			// Refresh 60 seconds before expiration
			refreshLead := 60 * time.Second
			if remaining > refreshLead {
				waitDuration = remaining - refreshLead
				log.Printf("[Auth] Access token expires in %v (at %s). Silent refresh scheduled in %v.",
					remaining.Round(time.Second), expTime.Format("15:04:05"), waitDuration.Round(time.Second))
			} else {
				log.Printf("[Auth] Access token is near or past expiration (remaining: %v). Refreshing immediately...", remaining.Round(time.Second))
				waitDuration = 0
			}
		}

		if waitDuration > 0 {
			select {
			case <-time.After(waitDuration):
			case <-tm.stopChan:
				log.Printf("[Auth] Silent token refresh manager stopped.")
				return
			}
		}

		// Perform token refresh
		log.Printf("[Auth] Performing silent token refresh via API...")
		newToken, err := tm.refresher.RefreshToken(tm.serverURL, rToken)
		if err != nil {
			log.Printf("[Auth] Silent token refresh failed: %v. Retrying in 10 seconds...", err)
			select {
			case <-time.After(10 * time.Second):
				continue
			case <-tm.stopChan:
				log.Printf("[Auth] Silent token refresh manager stopped.")
				return
			}
		}

		log.Printf("[Auth] Silent token refresh succeeded. New token active.")
		tm.updateToken(newToken)
	}
}
