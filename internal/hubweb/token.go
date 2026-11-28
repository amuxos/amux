package hubweb

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type sessionTokenClaims struct {
	TargetID       string `json:"target_id"`
	Name           string `json:"name"`
	Exp            int64  `json:"exp"`
	TurnID         string `json:"turn_id,omitempty"`
	ToolsOnly      bool   `json:"tools_only,omitempty"`
	ExpandProgress bool   `json:"expand_progress,omitempty"`
}

// SignSessionToken returns a URL-safe token scoped to one target/session until
// expiresAt. The signing secret itself is never embedded in the token.
func SignSessionToken(secret, targetID, name string, expiresAt time.Time) string {
	return signViewerToken(secret, sessionTokenClaims{TargetID: targetID, Name: name, Exp: expiresAt.Unix()})
}

// SignTurnProcessToken grants access to the presentation of exactly one Turn.
func SignTurnProcessToken(secret, targetID, name, turnID string, toolsOnly, expandProgress bool, expiresAt time.Time) string {
	if strings.TrimSpace(targetID) == "" || strings.TrimSpace(turnID) == "" {
		return ""
	}
	return signViewerToken(secret, sessionTokenClaims{
		TargetID: targetID, Name: name, TurnID: turnID, Exp: expiresAt.Unix(),
		ToolsOnly: toolsOnly, ExpandProgress: expandProgress,
	})
}

func signViewerToken(secret string, claims sessionTokenClaims) string {
	secret = strings.TrimSpace(secret)
	if secret == "" || strings.TrimSpace(claims.Name) == "" || claims.Exp <= 0 {
		return ""
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return ""
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(encodedPayload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encodedPayload + "." + sig
}

// VerifySessionToken checks that token is signed by secret, unexpired, and
// scoped to exactly targetID/name.
func VerifySessionToken(secret, token, targetID, name string, now time.Time) error {
	claims, err := SessionTokenScope(secret, token, now)
	if err != nil {
		return err
	}
	if claims.TargetID != targetID || claims.Name != name || claims.TurnID != "" {
		return errors.New("token scope mismatch")
	}
	return nil
}

// SessionTokenScope returns the target/session scope embedded in a signed token.
func SessionTokenScope(secret, token string, now time.Time) (sessionTokenClaims, error) {
	secret = strings.TrimSpace(secret)
	token = strings.TrimSpace(token)
	if secret == "" || token == "" {
		return sessionTokenClaims{}, errors.New("missing token")
	}
	payload, sig, ok := strings.Cut(token, ".")
	if !ok || payload == "" || sig == "" {
		return sessionTokenClaims{}, errors.New("malformed token")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(payload))
	wantSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(wantSig)) {
		return sessionTokenClaims{}, errors.New("invalid token signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return sessionTokenClaims{}, fmt.Errorf("decode token: %w", err)
	}
	var claims sessionTokenClaims
	if err := json.Unmarshal(raw, &claims); err != nil {
		return sessionTokenClaims{}, fmt.Errorf("decode token claims: %w", err)
	}
	if claims.Exp <= 0 || !now.Before(time.Unix(claims.Exp, 0)) {
		return sessionTokenClaims{}, errors.New("token expired")
	}
	return claims, nil
}
