package guestbridge

import (
	"context"
	"encoding/binary"
	"sync"
	"time"
)

const reportAcknowledgementTimeout = 2 * time.Second

// SET_REPORT in the device's no_out_endpoint f_hid mode has one overwriting
// buffer, not a queue. An ACK proves userspace consumed the previous report.
// ACKs are transport metadata only; AEAD still authenticates every message.
func reportAcknowledgement(report [ReportSize]byte) ([ReportSize]byte, bool) {
	var ack [ReportSize]byte
	n := int(binary.BigEndian.Uint16(report[6:8]))
	offset := binary.BigEndian.Uint32(report[12:16])
	total := binary.BigEndian.Uint32(report[16:20])
	if string(report[:4]) != "JKG1" || report[4] != 1 || report[5] != 0 || n < 1 || n > ReportSize-reportHeader || total == 0 || total > MaxWire || uint64(offset)+uint64(n) > uint64(total) {
		return ack, false
	}
	copy(ack[:4], "JKA1")
	ack[4] = 1
	copy(ack[8:16], report[8:16])
	return ack, true
}

type acknowledgedInput struct{ Reports }

// Framer calls this only after validating the report against its current
// message, offset and size limit. Never advance a stale transfer during recovery.
func (r *acknowledgedInput) Acknowledge(ctx context.Context, report [ReportSize]byte) error {
	ack, ok := reportAcknowledgement(report)
	if !ok {
		return ErrProtocol
	}
	ackCtx, cancel := context.WithTimeout(ctx, reportAcknowledgementTimeout)
	defer cancel()
	return r.Reports.WriteReport(ackCtx, ack)
}

// AcknowledgeOutputReports is used only on the device-side control receiver.
func AcknowledgeOutputReports(raw Reports) Reports { return &acknowledgedInput{raw} }

type acknowledgedOutput struct {
	raw       Reports
	ctx       context.Context
	cancel    context.CancelCauseFunc
	closeOnce sync.Once
	writeMu   sync.Mutex
	mu        sync.Mutex
	expected  [ReportSize]byte
	pending   chan struct{}
	data      chan [ReportSize]byte
}

func flowControlledOutputReports(raw Reports) Reports {
	ctx, cancel := context.WithCancelCause(context.Background())
	r := &acknowledgedOutput{raw: raw, ctx: ctx, cancel: cancel, data: make(chan [ReportSize]byte, 512)}
	go r.receive()
	return r
}
func (r *acknowledgedOutput) Close() error {
	var err error
	r.closeOnce.Do(func() { r.cancel(context.Canceled); err = r.raw.Close() })
	return err
}
func (r *acknowledgedOutput) receive() {
	for {
		report, err := r.raw.ReadReport(r.ctx)
		if err != nil {
			r.cancel(err)
			_ = r.Close()
			return
		}
		if string(report[:4]) == "JKA1" {
			r.mu.Lock()
			if r.pending != nil && report == r.expected {
				close(r.pending)
				r.pending = nil
			}
			r.mu.Unlock()
			continue
		}
		select {
		case r.data <- report:
		case <-r.ctx.Done():
			return
		}
	}
}
func (r *acknowledgedOutput) ReadReport(ctx context.Context) ([ReportSize]byte, error) {
	if err := ctx.Err(); err != nil {
		return [ReportSize]byte{}, err
	}
	select {
	case report := <-r.data:
		return report, nil
	case <-ctx.Done():
		return [ReportSize]byte{}, ctx.Err()
	case <-r.ctx.Done():
		return [ReportSize]byte{}, context.Cause(r.ctx)
	}
}
func (r *acknowledgedOutput) WriteReport(ctx context.Context, report [ReportSize]byte) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	ack, ok := reportAcknowledgement(report)
	if !ok {
		return ErrProtocol
	}
	ctx, cancel := context.WithTimeout(ctx, reportAcknowledgementTimeout)
	defer cancel()
	pending := make(chan struct{})
	r.mu.Lock()
	r.expected = ack
	r.pending = pending
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		if r.pending == pending {
			r.pending = nil
		}
		r.mu.Unlock()
	}()
	if err := r.raw.WriteReport(ctx, report); err != nil {
		_ = r.Close()
		return err
	}
	select {
	case <-pending:
		return ctx.Err()
	case <-ctx.Done():
		_ = r.Close()
		return ctx.Err()
	case <-r.ctx.Done():
		return context.Cause(r.ctx)
	}
}
