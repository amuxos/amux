package sessioninput

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/session"
)

func testPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := png.Encode(&out, image.NewRGBA(image.Rect(0, 0, width, height))); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestValidateImageInputLimitsAndContent(t *testing.T) {
	valid := testPNG(t, 2, 3)
	parts := []session.InputPart{{Text: "inspect"}}
	for range MaxImages {
		parts = append(parts, session.InputPart{Image: &session.ImageInput{Data: valid, MIMEType: "image/png"}})
	}
	if err := Validate(session.UserInput{Parts: parts}, true); err != nil {
		t.Fatalf("Validate exactly max images: %v", err)
	}
	parts = append(parts, session.InputPart{Image: &session.ImageInput{Data: valid, MIMEType: "image/png"}})
	if err := Validate(session.UserInput{Parts: parts}, true); err == nil || !strings.Contains(err.Error(), "image count") {
		t.Fatalf("Validate 11 images = %v, want count error", err)
	}
	if err := Validate(session.UserInput{Parts: []session.InputPart{{Image: &session.ImageInput{Data: valid, MIMEType: "image/jpeg"}}}}, true); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("Validate MIME mismatch = %v", err)
	}
	if err := Validate(session.UserInput{Parts: []session.InputPart{{Image: &session.ImageInput{Data: valid, MIMEType: "image/png"}}}}, false); err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("Validate unsupported model = %v", err)
	}
}

func TestValidateRejectsOversizedDimensionsWithoutAllocatingPixels(t *testing.T) {
	data := testPNG(t, 1, 1)
	// PNG IHDR width/height are bytes 16..23; DecodeConfig reads these without
	// allocating the declared pixel buffer.
	copy(data[16:20], []byte{0, 0, 0x13, 0x89})
	copy(data[20:24], []byte{0, 0, 0x13, 0x89})
	binary.BigEndian.PutUint32(data[29:33], crc32.ChecksumIEEE(data[12:29]))
	err := ValidateImage(data, "image/png")
	if err == nil || !strings.Contains(err.Error(), "dimensions") {
		t.Fatalf("ValidateImage oversized dimensions = %v", err)
	}
}
