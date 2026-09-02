package auth

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

func createTestJWT(exp int64) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payloadData, _ := json.Marshal(map[string]interface{}{
		"id":       1,
		"username": "testuser",
		"exp":      exp,
	})
	payload := base64.RawURLEncoding.EncodeToString(payloadData)
	sig := base64.RawURLEncoding.EncodeToString([]byte("signature"))
	return header + "." + payload + "." + sig
}

func TestParseJWTExpiration(t *testing.T) {
	expectedExp := time.Now().Add(15 * time.Minute).Truncate(time.Second)
	tokenStr := createTestJWT(expectedExp.Unix())

	exp, err := ParseJWTExpiration(tokenStr)
	if err != nil {
		t.Fatalf("unexpected error parsing JWT: %v", err)
	}

	if exp.Unix() != expectedExp.Unix() {
		t.Fatalf("expected exp %v, got %v", expectedExp.Unix(), exp.Unix())
	}
}

type mockRefresher struct {
	newToken string
	err      error
	called   chan struct{}
}

func (m *mockRefresher) RefreshToken(serverURL, refreshToken string) (string, error) {
	if m.called != nil {
		select {
		case m.called <- struct{}{}:
		default:
		}
	}
	return m.newToken, m.err
}

func TestTokenManager_SilentRefresh(t *testing.T) {
	// Token expiring in 62 seconds (should refresh in ~2s)
	initialExp := time.Now().Add(62 * time.Second).Unix()
	initialToken := createTestJWT(initialExp)
	newExp := time.Now().Add(15 * time.Minute).Unix()
	newToken := createTestJWT(newExp)

	refresher := &mockRefresher{
		newToken: newToken,
		called:   make(chan struct{}, 1),
	}

	user := User{ID: 1, Username: "testuser", Email: "test@example.com"}
	tm := NewTokenManager(refresher, "http://localhost:5001", initialToken, "refreshtoken123", user)

	refreshedNotified := make(chan string, 1)
	tm.OnTokenRefreshed(func(tok string) {
		refreshedNotified <- tok
	})

	tm.Start()
	defer tm.Stop()

	select {
	case <-refresher.called:
		// success
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for silent refresh")
	}

	select {
	case tok := <-refreshedNotified:
		if tok != newToken {
			t.Fatalf("expected new token %s, got %s", newToken, tok)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for token refresh callback")
	}

	if tm.GetToken() != newToken {
		t.Fatalf("expected tm.GetToken() = %s, got %s", newToken, tm.GetToken())
	}
}
