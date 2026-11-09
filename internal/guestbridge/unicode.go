package guestbridge

import (
	"fmt"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// DecodeClipboardUTF16 requires a terminator, rejects unpaired surrogates, and
// bounds the UTF-8 result even when the source allocation has spare capacity.
func DecodeClipboardUTF16(units []uint16) (string, error) {
	var out strings.Builder
	for i := 0; i < len(units); i++ {
		u := units[i]
		if u == 0 {
			return out.String(), nil
		}
		r := rune(u)
		if u >= 0xd800 && u <= 0xdbff {
			if i+1 >= len(units) || units[i+1] < 0xdc00 || units[i+1] > 0xdfff {
				return "", fmt.Errorf("clipboard contains invalid UTF-16")
			}
			i++
			r = utf16.DecodeRune(rune(u), rune(units[i]))
		} else if u >= 0xdc00 && u <= 0xdfff {
			return "", fmt.Errorf("clipboard contains invalid UTF-16")
		}
		if out.Len()+utf8.RuneLen(r) > MaxText {
			return "", fmt.Errorf("clipboard text exceeds %d UTF-8 bytes", MaxText)
		}
		out.WriteRune(r)
	}
	return "", fmt.Errorf("clipboard text is too large or has no terminator")
}

func EncodeClipboardUTF16(text string) ([]uint16, error) {
	if err := ValidateText(text); err != nil {
		return nil, err
	}
	return append(utf16.Encode([]rune(text)), 0), nil
}
