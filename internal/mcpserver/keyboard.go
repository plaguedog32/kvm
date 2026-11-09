package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strings"
	"unicode/utf16"
)

type report struct {
	modifier byte
	keys     [6]byte
}

func character(r rune) (report, error) {
	v := report{}
	switch {
	case r >= 'a' && r <= 'z':
		v.keys[0] = byte(r - 'a' + 4)
	case r >= 'A' && r <= 'Z':
		v.modifier = 2
		v.keys[0] = byte(r - 'A' + 4)
	case r >= '1' && r <= '9':
		v.keys[0] = byte(r - '1' + 30)
	case r == '0':
		v.keys[0] = 39
	case r == '\n':
		v.keys[0] = 40
	case r == '\t':
		v.keys[0] = 43
	case r == ' ':
		v.keys[0] = 44
	default:
		ids := []byte{45, 46, 47, 48, 49, 51, 52, 53, 54, 55, 56}
		if i := strings.IndexRune("-=[]\\;'`,./", r); i >= 0 {
			v.keys[0] = ids[i]
		} else if i := strings.IndexRune("_+{}|:\"~<>?", r); i >= 0 {
			v.keys[0] = ids[i]
			v.modifier = 2
		} else if i := strings.IndexRune("!@#$%^&*()", r); i >= 0 {
			v.keys[0] = byte(30 + i)
			if i == 9 {
				v.keys[0] = 39
			}
			v.modifier = 2
		} else {
			return v, fmt.Errorf("text requires US keyboard ASCII, newline or tab; use powershell for Unicode scripts")
		}
	}
	return v, nil
}

func keyReport(key string, modifiers []string) (report, error) {
	keys := map[string]byte{"Enter": 40, "Escape": 41, "Backspace": 42, "Tab": 43, "Space": 44, "CapsLock": 57, "PrintScreen": 70, "ScrollLock": 71, "Pause": 72, "Insert": 73, "Home": 74, "PageUp": 75, "Delete": 76, "End": 77, "PageDown": 78, "ArrowRight": 79, "ArrowLeft": 80, "ArrowDown": 81, "ArrowUp": 82}
	for i := 1; i <= 12; i++ {
		keys[fmt.Sprintf("F%d", i)] = byte(57 + i)
	}
	var v report
	if code, ok := keys[key]; ok {
		v.keys[0] = code
	} else if len(key) == 1 {
		var err error
		v, err = character(rune(key[0]))
		if err != nil {
			return v, err
		}
	} else {
		return v, fmt.Errorf("unknown key %q", key)
	}
	if len(modifiers) > 4 {
		return v, fmt.Errorf("at most four modifiers")
	}
	for _, name := range modifiers {
		mask, ok := map[string]byte{"Control": 1, "Shift": 2, "Alt": 4, "Meta": 8}[name]
		if !ok {
			return v, fmt.Errorf("unknown modifier %q", name)
		}
		v.modifier |= mask
	}
	return v, nil
}

func powershellCommand(script string) (string, error) {
	if script == "" {
		return "", fmt.Errorf("script is required")
	}
	units := utf16.Encode([]rune(script))
	raw := make([]byte, len(units)*2)
	for i, v := range units {
		binary.LittleEndian.PutUint16(raw[i*2:], v)
	}
	command := "powershell.exe -NoLogo -NoProfile -EncodedCommand " + base64.StdEncoding.EncodeToString(raw)
	if len(command) > 7500 {
		return "", fmt.Errorf("encoded command exceeds 7500 characters; split the script")
	}
	return command, nil
}

func (c *controller) tap(ctx context.Context, r report, delay int) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	defer func() {
		if releaseErr := c.backend.Keyboard(0, [6]byte{}); err == nil {
			err = releaseErr
		}
	}()
	if err := c.backend.Keyboard(r.modifier, r.keys); err != nil {
		return err
	}
	return wait(ctx, delay)
}

func (c *controller) typeText(ctx context.Context, text string, delay int) error {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	// Validate all text before sending any input.
	reports := make([]report, 0, len(text))
	for _, ch := range text {
		v, err := character(ch)
		if err != nil {
			return err
		}
		reports = append(reports, v)
	}
	if c.backend.CapsLock() {
		return fmt.Errorf("caps lock is on; turn it off before typing")
	}
	for _, v := range reports {
		if err := c.tap(ctx, v, delay); err != nil {
			return err
		}
		if err := wait(ctx, delay); err != nil {
			return err
		}
	}
	return nil
}
