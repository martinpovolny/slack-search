package slack

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestBuildCanvasRequestBinary(t *testing.T) {
	quipID := "ABC123def456"
	result := BuildCanvasRequestBinary(quipID)

	// Must be valid base64
	decoded, err := base64.StdEncoding.DecodeString(result)
	if err != nil {
		t.Fatalf("BuildCanvasRequestBinary returned invalid base64: %v", err)
	}

	// Must contain the quip ID in the binary
	if !strings.Contains(string(decoded), quipID) {
		t.Error("decoded protobuf does not contain the quip ID")
	}

	// Must contain "editor" string
	if !strings.Contains(string(decoded), "editor") {
		t.Error("decoded protobuf does not contain 'editor'")
	}

	// Must be non-empty and reasonable size
	if len(decoded) < 20 {
		t.Errorf("decoded protobuf too short: %d bytes", len(decoded))
	}
}

func TestBuildCanvasRequestBinaryDeterministic(t *testing.T) {
	quipID := "testQuipId"
	r1 := BuildCanvasRequestBinary(quipID)
	r2 := BuildCanvasRequestBinary(quipID)
	if r1 != r2 {
		t.Error("BuildCanvasRequestBinary is not deterministic")
	}
}

func TestEncodeVarint(t *testing.T) {
	tests := []struct {
		input    int
		expected []byte
	}{
		{0, []byte{0}},
		{1, []byte{1}},
		{127, []byte{127}},
		{128, []byte{0x80, 0x01}},
		{300, []byte{0xAC, 0x02}},
	}

	for _, tt := range tests {
		got := encodeVarint(tt.input)
		if len(got) != len(tt.expected) {
			t.Errorf("encodeVarint(%d): got %v, want %v", tt.input, got, tt.expected)
			continue
		}
		for i := range got {
			if got[i] != tt.expected[i] {
				t.Errorf("encodeVarint(%d): byte %d: got %02x, want %02x", tt.input, i, got[i], tt.expected[i])
			}
		}
	}
}

func TestExtractCanvasText(t *testing.T) {
	// Build a byte sequence that contains printable strings interspersed with binary data
	var raw []byte

	// Add some binary noise
	raw = append(raw, 0x08, 0x01, 0x12, 0x0A)

	// Add a meaningful string (>= 15 chars, >= 10 after filtering)
	content := "This is a real canvas content line for testing purposes"
	raw = append(raw, []byte(content)...)

	// Add more binary noise
	raw = append(raw, 0x00, 0x00, 0x18, 0x01)

	// Add a base64-ish string that should be filtered out
	b64ish := "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	raw = append(raw, []byte(b64ish)...)

	// Add more binary noise
	raw = append(raw, 0x00, 0x00)

	// Add another meaningful string
	content2 := "Second paragraph of canvas content here"
	raw = append(raw, []byte(content2)...)

	plainText, htmlContent := ExtractCanvasText(raw)

	if plainText == "" {
		t.Error("ExtractCanvasText returned empty plain text")
	}
	if htmlContent == "" {
		t.Error("ExtractCanvasText returned empty HTML content")
	}

	// The meaningful strings should appear in the output
	if !strings.Contains(plainText, "canvas content") {
		t.Errorf("plain text missing expected content, got: %s", plainText)
	}
}

func TestExtractCanvasTextEmpty(t *testing.T) {
	plain, html := ExtractCanvasText([]byte{0x01, 0x02, 0x03})
	if plain != "" {
		t.Errorf("expected empty plain text for binary input, got: %s", plain)
	}
	if html != "" {
		t.Errorf("expected empty html for binary input, got: %s", html)
	}
}
