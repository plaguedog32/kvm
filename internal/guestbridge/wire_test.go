package guestbridge

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestClipboardWireExpansionIsBoundedForControlCharacters(t *testing.T) {
	text := strings.Repeat("\x01", MaxText)
	response := Response{ID: 1, Unlocked: true, HasText: true, Text: text, ClipboardSequence: 4294967295}
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > MaxText*4/3+1024 || bytes.Contains(data, []byte(`\u0001`)) {
		t.Fatal("clipboard wire expansion is not bounded")
	}
	var decoded Response
	if err := json.Unmarshal(data, &decoded); err != nil || decoded != response {
		t.Fatal("response text did not roundtrip", err)
	}
	request := Request{ID: 2, Method: "set_clipboard", Text: "日本語\r\n🐕"}
	data, err = json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var decodedRequest Request
	if err := json.Unmarshal(data, &decodedRequest); err != nil || decodedRequest != request {
		t.Fatal("request text did not roundtrip", err)
	}
	for _, invalid := range []string{
		`{"id":1,"text":"!bad base64!"}`,
		`{"id":1,"text":"AA=="}`,
		`{"id":1,"text":"/w=="}`,
		`{"id":1,"text":"","unknown":true}`,
	} {
		if err := json.Unmarshal([]byte(invalid), &decoded); err == nil {
			t.Fatal("invalid clipboard wire data accepted")
		}
	}
}
