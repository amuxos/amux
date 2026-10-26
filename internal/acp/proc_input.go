package acp

import (
	"encoding/base64"

	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/sessioninput"
)

// InputCapabilities reports the immutable initialize negotiation. An agent
// must explicitly opt in to image content; an absent capability is text-only.
func (p *Proc) InputCapabilities() ([]string, string) {
	modalities := []string{"text"}
	if p.imageInput {
		modalities = append(modalities, "image")
	}
	return modalities, p.inputCapabilitySource
}

func promptContent(input session.UserInput, imageSupported bool) ([]map[string]string, error) {
	if err := sessioninput.Validate(input, imageSupported); err != nil {
		return nil, err
	}
	content := make([]map[string]string, 0, len(input.Parts))
	for _, part := range input.Parts {
		if part.Image != nil {
			content = append(content, map[string]string{
				"type": "image", "mimeType": part.Image.MIMEType,
				"data": base64.StdEncoding.EncodeToString(part.Image.Data),
			})
		} else if part.Text != "" {
			content = append(content, map[string]string{"type": "text", "text": part.Text})
		}
	}
	return content, nil
}
