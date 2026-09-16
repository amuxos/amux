package sessioninput

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"strings"

	"github.com/amuxos/amux/internal/session"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

const (
	MaxImages     = 10
	MaxImageBytes = 10 << 20
	MaxTotalBytes = 20 << 20
	MaxPixels     = 25_000_000
)

func FromProto(parts []*pb.InputPart, fallbackText string) (session.UserInput, error) {
	if len(parts) == 0 {
		return session.TextUserInput(fallbackText), nil
	}
	input := session.UserInput{Parts: make([]session.InputPart, 0, len(parts))}
	for i, part := range parts {
		if part == nil {
			return session.UserInput{}, fmt.Errorf("input part %d is empty", i)
		}
		switch value := part.Value.(type) {
		case *pb.InputPart_Text:
			input.Parts = append(input.Parts, session.InputPart{Text: value.Text})
		case *pb.InputPart_Image:
			if value.Image == nil {
				return session.UserInput{}, fmt.Errorf("input part %d has an empty image", i)
			}
			input.Parts = append(input.Parts, session.InputPart{Image: &session.ImageInput{
				Data: append([]byte(nil), value.Image.Data...), MIMEType: value.Image.MimeType, DisplayName: value.Image.DisplayName,
			}})
		default:
			return session.UserInput{}, fmt.Errorf("input part %d has no value", i)
		}
	}
	return input, nil
}

func Validate(input session.UserInput, imageSupported bool) error {
	images, total := 0, 0
	for i, part := range input.Parts {
		if part.Image == nil {
			continue
		}
		images++
		if !imageSupported {
			return errors.New("current session model does not support image input")
		}
		if images > MaxImages {
			return fmt.Errorf("image count exceeds limit %d", MaxImages)
		}
		if len(part.Image.Data) > MaxImageBytes {
			return fmt.Errorf("image %d exceeds %d bytes", images, MaxImageBytes)
		}
		total += len(part.Image.Data)
		if total > MaxTotalBytes {
			return fmt.Errorf("image input exceeds total limit %d bytes", MaxTotalBytes)
		}
		width, height, detected, err := imageInfo(part.Image.Data)
		if err != nil {
			return fmt.Errorf("image %d: %w", images, err)
		}
		if strings.TrimSpace(part.Image.MIMEType) != detected {
			return fmt.Errorf("image %d MIME type %q does not match content %q", images, part.Image.MIMEType, detected)
		}
		if width <= 0 || height <= 0 || int64(width)*int64(height) > MaxPixels {
			return fmt.Errorf("image %d dimensions %dx%d exceed %d pixels", images, width, height, MaxPixels)
		}
		_ = i
	}
	if len(input.Parts) == 0 || (strings.TrimSpace(input.Text()) == "" && images == 0) {
		return errors.New("input is empty")
	}
	return nil
}

func ValidateImage(data []byte, mimeType string) error {
	return Validate(session.UserInput{Parts: []session.InputPart{{Image: &session.ImageInput{
		Data: data, MIMEType: mimeType,
	}}}}, true)
}

func SupportsImage(modalities []string) bool {
	for _, modality := range modalities {
		if strings.EqualFold(strings.TrimSpace(modality), "image") {
			return true
		}
	}
	return false
}

func imageInfo(data []byte) (int, int, string, error) {
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		w, h, err := webPDimensions(data)
		return w, h, "image/webp", err
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0, "", errors.New("unsupported or invalid image; JPEG, PNG, and WebP are accepted")
	}
	switch format {
	case "jpeg":
		return config.Width, config.Height, "image/jpeg", nil
	case "png":
		return config.Width, config.Height, "image/png", nil
	default:
		return 0, 0, "", errors.New("unsupported image format")
	}
}

func webPDimensions(data []byte) (int, int, error) {
	if len(data) < 30 {
		return 0, 0, errors.New("invalid WebP image")
	}
	switch string(data[12:16]) {
	case "VP8X":
		w := 1 + int(data[24]) + int(data[25])<<8 + int(data[26])<<16
		h := 1 + int(data[27]) + int(data[28])<<8 + int(data[29])<<16
		return w, h, nil
	case "VP8L":
		if len(data) < 25 || data[20] != 0x2f {
			return 0, 0, errors.New("invalid lossless WebP image")
		}
		bits := binary.LittleEndian.Uint32(data[21:25])
		return 1 + int(bits&0x3fff), 1 + int((bits>>14)&0x3fff), nil
	case "VP8 ":
		if len(data) < 30 || data[23] != 0x9d || data[24] != 0x01 || data[25] != 0x2a {
			return 0, 0, errors.New("invalid lossy WebP image")
		}
		return int(binary.LittleEndian.Uint16(data[26:28]) & 0x3fff), int(binary.LittleEndian.Uint16(data[28:30]) & 0x3fff), nil
	default:
		return 0, 0, errors.New("unsupported WebP image")
	}
}
