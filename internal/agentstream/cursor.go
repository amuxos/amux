package agentstream

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

type cursorToken struct {
	Version int    `json:"v"`
	Epoch   string `json:"e"`
	Seq     uint64 `json:"s"`
	Check   string `json:"c"`
}

// EncodeCursor creates an opaque transport token. The checksum detects
// truncation/corruption; authorization remains the enclosing Session read.
func EncodeCursor(cursor Cursor) (string, error) {
	if cursor == (Cursor{}) {
		return "", nil
	}
	if cursor.Epoch == "" {
		return "", ErrInvalidCursor
	}
	token := cursorToken{Version: 1, Epoch: cursor.Epoch, Seq: cursor.Seq}
	token.Check = cursorChecksum(token.Version, token.Epoch, token.Seq)
	raw, err := json.Marshal(token)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func DecodeCursor(value string) (Cursor, error) {
	if value == "" {
		return Cursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return Cursor{}, ErrInvalidCursor
	}
	var token cursorToken
	if err := strictUnmarshal(raw, &token); err != nil || token.Version != 1 || token.Epoch == "" ||
		token.Check != cursorChecksum(token.Version, token.Epoch, token.Seq) {
		return Cursor{}, ErrInvalidCursor
	}
	return Cursor{Epoch: token.Epoch, Seq: token.Seq}, nil
}

func cursorChecksum(version int, epoch string, seq uint64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%d", version, epoch, seq)))
	return hex.EncodeToString(sum[:8])
}
