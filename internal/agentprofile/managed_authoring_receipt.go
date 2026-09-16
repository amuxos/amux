package agentprofile

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type ManagedAuthoringReceiptClaims struct {
	SessionID            string                    `json:"session_id"`
	PreviewTurnID        string                    `json:"preview_turn_id"`
	Operation            ManagedAuthoringOperation `json:"operation"`
	TargetRef            string                    `json:"target_ref"`
	SourceRef            string                    `json:"source_ref,omitempty"`
	BaseRevision         string                    `json:"base_revision"`
	PatchDigest          string                    `json:"patch_digest"`
	Tree                 string                    `json:"tree"`
	EvaluationProfileRef string                    `json:"evaluation_profile_ref,omitempty"`
	ExpiresAt            int64                     `json:"expires_at"`
}

type ManagedAuthoringReceiptSigner struct {
	key []byte
	ttl time.Duration
	now func() time.Time
}

func NewManagedAuthoringReceiptSigner(key []byte, ttl time.Duration, now func() time.Time) (*ManagedAuthoringReceiptSigner, error) {
	if len(key) < 32 {
		return nil, fmt.Errorf("managed authoring receipt key must be at least 32 bytes")
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("managed authoring receipt ttl must be positive")
	}
	if now == nil {
		now = time.Now
	}
	return &ManagedAuthoringReceiptSigner{key: append([]byte(nil), key...), ttl: ttl, now: now}, nil
}

func (s *ManagedAuthoringReceiptSigner) Issue(claims ManagedAuthoringReceiptClaims) (string, error) {
	if s == nil {
		return "", fmt.Errorf("managed authoring receipt signer is unavailable")
	}
	if err := validateManagedAuthoringReceiptClaims(claims, false); err != nil {
		return "", err
	}
	claims.ExpiresAt = s.now().Add(s.ttl).Unix()
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return encoded + "." + base64.RawURLEncoding.EncodeToString(s.sign(encoded)), nil
}

func (s *ManagedAuthoringReceiptSigner) Verify(receipt string) (ManagedAuthoringReceiptClaims, error) {
	if s == nil {
		return ManagedAuthoringReceiptClaims{}, fmt.Errorf("managed authoring receipt signer is unavailable")
	}
	payloadText, signatureText, ok := strings.Cut(strings.TrimSpace(receipt), ".")
	if !ok || payloadText == "" || signatureText == "" {
		return ManagedAuthoringReceiptClaims{}, fmt.Errorf("invalid managed authoring preview receipt")
	}
	signature, err := base64.RawURLEncoding.DecodeString(signatureText)
	if err != nil || !hmac.Equal(signature, s.sign(payloadText)) {
		return ManagedAuthoringReceiptClaims{}, fmt.Errorf("invalid managed authoring preview receipt signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadText)
	if err != nil {
		return ManagedAuthoringReceiptClaims{}, fmt.Errorf("decode managed authoring preview receipt: %w", err)
	}
	var claims ManagedAuthoringReceiptClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ManagedAuthoringReceiptClaims{}, fmt.Errorf("decode managed authoring preview receipt: %w", err)
	}
	if err := validateManagedAuthoringReceiptClaims(claims, true); err != nil {
		return ManagedAuthoringReceiptClaims{}, err
	}
	if s.now().Unix() >= claims.ExpiresAt {
		return ManagedAuthoringReceiptClaims{}, fmt.Errorf("managed authoring preview receipt expired")
	}
	return claims, nil
}

func (s *ManagedAuthoringReceiptSigner) sign(payload string) []byte {
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write([]byte(payload))
	return mac.Sum(nil)
}

func validateManagedAuthoringReceiptClaims(claims ManagedAuthoringReceiptClaims, requireExpiry bool) error {
	if strings.TrimSpace(claims.SessionID) == "" || strings.TrimSpace(claims.PreviewTurnID) == "" {
		return fmt.Errorf("managed authoring receipt session and preview turn are required")
	}
	if _, _, err := normalizeManagedAuthoringRequest(ManagedAuthoringRequest{
		Operation: claims.Operation, TargetRef: claims.TargetRef, SourceRef: claims.SourceRef,
		BaseRevision: claims.BaseRevision,
	}, true); err != nil {
		return err
	}
	if len(claims.PatchDigest) != sha256.Size*2 {
		return fmt.Errorf("managed authoring receipt patch digest is invalid")
	}
	if !managedRevisionPattern.MatchString(claims.Tree) {
		return fmt.Errorf("managed authoring receipt tree is invalid")
	}
	if claims.EvaluationProfileRef != "" {
		namespace, name, ok := strings.Cut(claims.EvaluationProfileRef, "/")
		if !ok || !isManagedEvaluationNamespace(namespace) || validateManagedProfileName(name) != nil {
			return fmt.Errorf("managed authoring receipt evaluation profile is invalid")
		}
	}
	if requireExpiry && claims.ExpiresAt <= 0 {
		return fmt.Errorf("managed authoring receipt expiry is invalid")
	}
	return nil
}
