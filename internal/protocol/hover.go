package protocol

import (
	"encoding/json"
	"fmt"
	"strings"
)

// UnmarshalJSON accepts both current MarkupContent and legacy MarkedString
// hover shapes without modifying the generated protocol model.
func (h *Hover) UnmarshalJSON(data []byte) error {
	var wire struct {
		Contents json.RawMessage `json:"contents"`
		Range    Range           `json:"range,omitempty"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	h.Range = wire.Range
	h.Contents = MarkupContent{}
	if len(wire.Contents) == 0 || string(wire.Contents) == "null" {
		return nil
	}
	var markup MarkupContent
	if err := json.Unmarshal(wire.Contents, &markup); err == nil && markup.Kind != "" {
		h.Contents = markup
		return nil
	}
	parts := []json.RawMessage{wire.Contents}
	if wire.Contents[0] == '[' {
		if err := json.Unmarshal(wire.Contents, &parts); err != nil {
			return err
		}
	}
	var rendered []string
	for _, part := range parts {
		var text string
		if err := json.Unmarshal(part, &text); err == nil {
			rendered = append(rendered, text)
			continue
		}
		var marked struct {
			Language string  `json:"language"`
			Value    *string `json:"value"`
		}
		if err := json.Unmarshal(part, &marked); err != nil || marked.Value == nil {
			return fmt.Errorf("unsupported hover content: %s", part)
		}
		if marked.Language == "" {
			rendered = append(rendered, *marked.Value)
		} else {
			rendered = append(rendered, "```"+marked.Language+"\n"+*marked.Value+"\n```")
		}
	}
	h.Contents = MarkupContent{Kind: Markdown, Value: strings.Join(rendered, "\n\n")}
	return nil
}
