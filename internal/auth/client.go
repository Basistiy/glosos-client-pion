package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client handles HTTP API authentication requests to the Glosos backend.
type Client struct {
	HTTPClient *http.Client
}

// NewClient creates a new HTTP Auth Client.
func NewClient(timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &Client{
		HTTPClient: &http.Client{
			Timeout: timeout,
		},
	}
}

func sanitizeURL(serverURL string) string {
	return strings.TrimRight(serverURL, "/")
}

// Login authenticates a user with login/email and password against /api/auth/login.
func (c *Client) Login(serverURL, loginIdentifier, password string) (*LoginResponse, error) {
	baseURL := sanitizeURL(serverURL)
	endpoints := []string{
		baseURL + "/api/auth/login",
		baseURL + "/auth/login",
	}

	reqBody, err := json.Marshal(LoginRequest{
		Login:    loginIdentifier,
		Password: password,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal login request: %w", err)
	}

	var lastErr error
	for _, endpoint := range endpoints {
		req, err := http.NewRequest("POST", endpoint, bytes.NewBuffer(reqBody))
		if err != nil {
			return nil, fmt.Errorf("failed to create http request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "Glosos-Pion-Client/1.0")

		resp, err := c.HTTPClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()

		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			lastErr = fmt.Errorf("failed to read response body: %w", err)
			continue
		}

		if resp.StatusCode == http.StatusNotFound {
			lastErr = fmt.Errorf("endpoint %s returned 404", endpoint)
			continue
		}

		var loginResp LoginResponse
		if err := json.Unmarshal(bodyBytes, &loginResp); err != nil {
			return nil, fmt.Errorf("failed to decode login response (status %d): %w", resp.StatusCode, err)
		}

		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
			errMsg := loginResp.Error
			if errMsg == "" {
				errMsg = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(bodyBytes))
			}
			return nil, fmt.Errorf("login failed: %s", errMsg)
		}

		if loginResp.Token == "" {
			return nil, errors.New("login failed: received empty access token")
		}

		return &loginResp, nil
	}

	return nil, fmt.Errorf("login failed across endpoints: %w", lastErr)
}

// RefreshToken requests a renewed JWT access token using a refresh token.
func (c *Client) RefreshToken(serverURL, refreshToken string) (string, error) {
	baseURL := sanitizeURL(serverURL)
	endpoints := []string{
		baseURL + "/api/auth/refresh",
		baseURL + "/auth/refresh",
	}

	reqBody, err := json.Marshal(RefreshRequest{
		RefreshToken: refreshToken,
	})
	if err != nil {
		return "", fmt.Errorf("failed to marshal refresh request: %w", err)
	}

	var lastErr error
	for _, endpoint := range endpoints {
		req, err := http.NewRequest("POST", endpoint, bytes.NewBuffer(reqBody))
		if err != nil {
			return "", fmt.Errorf("failed to create http request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "Glosos-Pion-Client/1.0")

		resp, err := c.HTTPClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()

		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			lastErr = fmt.Errorf("failed to read response body: %w", err)
			continue
		}

		if resp.StatusCode == http.StatusNotFound {
			lastErr = fmt.Errorf("endpoint %s returned 404", endpoint)
			continue
		}

		var refreshResp RefreshResponse
		if err := json.Unmarshal(bodyBytes, &refreshResp); err != nil {
			return "", fmt.Errorf("failed to decode refresh response (status %d): %w", resp.StatusCode, err)
		}

		if resp.StatusCode != http.StatusOK {
			errMsg := refreshResp.Error
			if errMsg == "" {
				errMsg = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(bodyBytes))
			}
			return "", fmt.Errorf("refresh token failed: %s", errMsg)
		}

		if refreshResp.Token == "" {
			return "", errors.New("refresh failed: received empty access token")
		}

		return refreshResp.Token, nil
	}

	return "", fmt.Errorf("token refresh failed across endpoints: %w", lastErr)
}

// GetTurnCredentials fetches ephemeral TURN credentials from /api/turn/credentials.
func (c *Client) GetTurnCredentials(serverURL, token string) (*TurnCredentialsResponse, error) {
	baseURL := sanitizeURL(serverURL)
	endpoints := []string{
		baseURL + "/api/turn/credentials",
		baseURL + "/turn/credentials",
	}

	var lastErr error
	for _, endpoint := range endpoints {
		req, err := http.NewRequest("GET", endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create turn request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("User-Agent", "Glosos-Pion-Client/1.0")

		resp, err := c.HTTPClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()

		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			lastErr = fmt.Errorf("failed to read response body: %w", err)
			continue
		}

		if resp.StatusCode == http.StatusNotFound {
			lastErr = fmt.Errorf("endpoint %s returned 404", endpoint)
			continue
		}

		var turnResp TurnCredentialsResponse
		if err := json.Unmarshal(bodyBytes, &turnResp); err != nil {
			return nil, fmt.Errorf("failed to decode turn response (status %d): %w", resp.StatusCode, err)
		}

		if resp.StatusCode != http.StatusOK {
			errMsg := turnResp.Error
			if errMsg == "" {
				errMsg = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(bodyBytes))
			}
			return nil, fmt.Errorf("get turn credentials failed: %s", errMsg)
		}

		return &turnResp, nil
	}

	return nil, fmt.Errorf("get turn credentials failed across endpoints: %w", lastErr)
}
