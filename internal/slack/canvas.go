package slack

import (
	"encoding/base64"
	"regexp"
	"strings"
)

// encodeVarint encodes an integer as a protobuf varint.
func encodeVarint(value int) []byte {
	var result []byte
	for value > 0x7F {
		result = append(result, byte((value&0x7F)|0x80))
		value >>= 7
	}
	result = append(result, byte(value))
	return result
}

// encodeStringField encodes a protobuf string field (wire type 2).
func encodeStringField(fieldNum int, s string) []byte {
	tag := (fieldNum << 3) | 2
	data := []byte(s)
	var buf []byte
	buf = append(buf, encodeVarint(tag)...)
	buf = append(buf, encodeVarint(len(data))...)
	buf = append(buf, data...)
	return buf
}

// encodeVarintField encodes a protobuf varint field (wire type 0).
func encodeVarintField(fieldNum int, value int) []byte {
	tag := (fieldNum << 3) | 0
	var buf []byte
	buf = append(buf, encodeVarint(tag)...)
	buf = append(buf, encodeVarint(value)...)
	return buf
}

// BuildCanvasRequestBinary constructs the protobuf request_binary for
// canvas/-/load-data/editor/1 and returns it as base64.
func BuildCanvasRequestBinary(quipID string) string {
	sub := append(encodeVarintField(1, 1), encodeStringField(2, quipID)...)
	field3Tag := encodeVarint((3 << 3) | 2)
	var field3 []byte
	field3 = append(field3, field3Tag...)
	field3 = append(field3, encodeVarint(len(sub))...)
	field3 = append(field3, sub...)

	var msg []byte
	msg = append(msg, encodeStringField(1, quipID)...)
	msg = append(msg, field3...)
	msg = append(msg, encodeStringField(5, "editor")...)
	msg = append(msg, encodeVarintField(6, 1)...)

	return base64.StdEncoding.EncodeToString(msg)
}

// Precompiled regexes for ExtractCanvasText.
var (
	rePrintable     = regexp.MustCompile(`[\x20-\x7e]{15,}`)
	reTemp          = regexp.MustCompile(`temp:`)
	reStructPrefix  = regexp.MustCompile(`^(aal|aTY|IfB|dbB|LGU|cSF|GU9|ke8)`)
	reLongHex       = regexp.MustCompile(`^[0-9a-f]{20,}$`)
	reBase64ish     = regexp.MustCompile(`^[A-Za-z0-9/+=]{15,}$`)
	reSuPrefix      = regexp.MustCompile(`^su:|^zzzzzz-|^Section/`)
	reHexDash       = regexp.MustCompile(`^[0-9a-f]{8,}-\d+$`)
	reParenHex      = regexp.MustCompile(`^\([0-9a-f]{30,}$`)
	reEDigits       = regexp.MustCompile(`^E\d{9,}`)
	reUpperUnderscore = regexp.MustCompile(`^[A-Z_]{10,}$`)
	reTrailingJL    = regexp.MustCompile(`jL$`)
	reHTMLTag       = regexp.MustCompile(`<[^>]+>`)
	reLeadingProto  = regexp.MustCompile(`^[A-Z^>]\s*([A-Z])`)
	reLeadingNonAlpha = regexp.MustCompile(`^[^a-zA-Z0-9("]+`)
)

// ExtractCanvasText extracts readable text and HTML content from a protobuf response.
// Returns (plainText, htmlContent).
func ExtractCanvasText(raw []byte) (string, string) {
	matches := rePrintable.FindAll(raw, -1)

	var lines []string
	var htmlParts []string

	for _, s := range matches {
		text := string(s)

		// Skip structural/internal data
		if len(text) >= 20 && reTemp.MatchString(text[:20]) {
			continue
		}
		if reStructPrefix.MatchString(text) {
			continue
		}
		if reLongHex.MatchString(text) {
			continue
		}
		if reBase64ish.MatchString(text) && !strings.Contains(text, " ") {
			continue
		}
		if reSuPrefix.MatchString(text) {
			continue
		}
		if reHexDash.MatchString(text) {
			continue
		}
		if reParenHex.MatchString(text) {
			continue
		}
		if reEDigits.MatchString(text) && !strings.Contains(text, " ") {
			continue
		}
		if reUpperUnderscore.MatchString(text) {
			continue
		}

		// Clean trailing protobuf markers
		text = strings.TrimSpace(reTrailingJL.ReplaceAllString(text, ""))
		if text == "" || len(text) < 10 {
			continue
		}

		// Store HTML version
		htmlParts = append(htmlParts, text)

		// Strip HTML for plain text
		plain := strings.TrimSpace(reHTMLTag.ReplaceAllString(text, ""))
		// Strip leading protobuf length bytes (preserve the captured uppercase letter)
		plain = reLeadingProto.ReplaceAllString(plain, "$1")
		// Strip leading non-alpha chars
		plain = reLeadingNonAlpha.ReplaceAllString(plain, "")
		if plain != "" && len(plain) > 8 {
			lines = append(lines, plain)
		}
	}

	return strings.Join(lines, "\n"), strings.Join(htmlParts, "\n")
}
