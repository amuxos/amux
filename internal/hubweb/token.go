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
	TargetID string `json:"target_id"`
	Name     string `json:"name"`
	Exp      int64  `json:"exp"`
}

// SignSessionToken returns a URL-safe token scoped to one target/session until
// expiresAt. The signing secret itself is never embedded in the token.
func SignSessionToken(secret, targetID, name string, expiresAt time.Time) string {
	secret = strings.TrimSpace(secret)
	if secret == "" || strings.TrimSpace(name) == "" || expiresAt.IsZero() {
		return ""
	}
	claims := sessionTokenClaims{TargetID: targetID, Name: name, Exp: expiresAt.Unix()}
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
	if claims.TargetID != targetID || claims.Name != name {
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
