package guestbridge

import (
	"bytes"
	"encoding/json"
	"io"
)

// Keep text strings at API/clipboard boundaries, but encode bytes as base64
// in both USB and local-pipe JSON. Control characters cannot amplify traffic.
type requestWire struct {
	ID     uint64 `json:"id"`
	Method string `json:"method"`
	Text   []byte `json:"text,omitempty"`
}
type responseWire struct {
	NativeCopy        bool   `json:"native_copy,omitempty"`
	ClipboardSequence uint32 `json:"clipboard_sequence,omitempty"`
	ID                uint64 `json:"id"`
	Error             string `json:"error,omitempty"`
	Text              []byte `json:"text,omitempty"`
	HasText           bool   `json:"has_text,omitempty"`
	User              string `json:"user,omitempty"`
	Unlocked          bool   `json:"unlocked"`
}

func decodeWire(data []byte, value any) error {
	if len(data) > MaxWire {
		return ErrProtocol
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return ErrProtocol
	}
	if decoder.Decode(new(any)) != io.EOF {
		return ErrProtocol
	}
	return nil
}
func (r Request) MarshalJSON() ([]byte, error) {
	if err := ValidateText(r.Text); err != nil {
		return nil, err
	}
	return json.Marshal(requestWire{ID: r.ID, Method: r.Method, Text: []byte(r.Text)})
}
func (r *Request) UnmarshalJSON(data []byte) error {
	var wire requestWire
	if err := decodeWire(data, &wire); err != nil {
		return err
	}
	text := string(wire.Text)
	if err := ValidateText(text); err != nil {
		return err
	}
	*r = Request{ID: wire.ID, Method: wire.Method, Text: text}
	return nil
}
func (r Response) MarshalJSON() ([]byte, error) {
	if err := ValidateText(r.Text); err != nil {
		return nil, err
	}
	return json.Marshal(responseWire{NativeCopy: r.NativeCopy, ClipboardSequence: r.ClipboardSequence, ID: r.ID, Error: r.Error, Text: []byte(r.Text), HasText: r.HasText, User: r.User, Unlocked: r.Unlocked})
}
func (r *Response) UnmarshalJSON(data []byte) error {
	var wire responseWire
	if err := decodeWire(data, &wire); err != nil {
		return err
	}
	text := string(wire.Text)
	if err := ValidateText(text); err != nil {
		return err
	}
	*r = Response{NativeCopy: wire.NativeCopy, ClipboardSequence: wire.ClipboardSequence, ID: wire.ID, Error: wire.Error, Text: text, HasText: wire.HasText, User: wire.User, Unlocked: wire.Unlocked}
	return nil
}
