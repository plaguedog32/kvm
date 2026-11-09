//go:build linux

package guestbridge

import (
	"context"
	"errors"
	"time"
)

var ErrNoClipboardChange = errors.New("remote application did not copy new text")

// WithExistingClipboard performs ordinary HID paste while retaining all of the
// guest clipboard's formats. No clipboard content is read or replaced.
func (m *Manager) WithExistingClipboard(ctx context.Context, paste func(context.Context) error) error {
	return m.operation(ctx, func(ctx context.Context, session *deviceSession) error {
		response, err := session.request(session.ctx, "status", "")
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if response.Error != "" {
			return errors.New(response.Error)
		}
		if !response.Unlocked {
			return ErrLocked
		}
		if session.revoked() {
			return ErrUnavailable
		}
		return paste(ctx)
	})
}

// CopyClipboard reads only after the caller's explicit copy action changes the
// remote clipboard. A no-op copy must not export an unrelated old clipboard.
func (m *Manager) CopyClipboard(ctx context.Context, copyKey func(context.Context) error) (Response, error) {
	var result Response
	err := m.operation(ctx, func(ctx context.Context, session *deviceSession) error {

		if !session.nativeCopy {
			return errors.New("guest companion does not support native copy")
		}
		state := func(ctx context.Context) (Response, error) {
			if err := ctx.Err(); err != nil {
				return Response{}, err
			}
			// Drain an in-flight response even if the browser goes away. Individual
			// requests keep their normal bounded deadline and session/revocation context.
			response, err := session.request(session.ctx, "clipboard_state", "")
			if err != nil {
				return Response{}, err
			}
			if err := ctx.Err(); err != nil {
				return Response{}, err
			}
			m.response(session, response)
			if response.Error != "" {
				return Response{}, errors.New(response.Error)
			}
			if !response.Unlocked {
				return Response{}, ErrLocked
			}
			return response, nil
		}
		before, err := state(ctx)
		if err != nil {
			return err
		}
		if session.revoked() {
			return ErrUnavailable
		}
		if err := copyKey(ctx); err != nil {
			return err
		}
		deadline := time.Now().Add(3 * time.Second)
		var last uint32
		var stableSince time.Time
		observedChange := false
		for time.Now().Before(deadline) {
			timer := time.NewTimer(30 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			after, err := state(ctx)
			if err != nil {
				return err
			}
			if after.ClipboardSequence != before.ClipboardSequence {
				if !observedChange {
					observedChange = true
					if time.Until(deadline) < 200*time.Millisecond {
						deadline = time.Now().Add(200 * time.Millisecond)
					}
				}
				if after.ClipboardSequence != last {
					last = after.ClipboardSequence
					stableSince = time.Now()
				}
				if time.Since(stableSince) < 100*time.Millisecond {
					continue
				}
				response, err := session.request(session.ctx, "copy_clipboard", "")
				if err != nil {
					return err
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				m.response(session, response)
				if response.Error != "" {
					return errors.New(response.Error)
				}
				if !response.Unlocked {
					return ErrLocked
				}
				if response.ClipboardSequence != after.ClipboardSequence {
					last = response.ClipboardSequence
					stableSince = time.Now()
					continue
				}
				result = response
				return nil
			}
		}
		if observedChange {
			return errors.New("remote clipboard kept changing; try again")
		}
		return ErrNoClipboardChange
	})
	return result, err
}
