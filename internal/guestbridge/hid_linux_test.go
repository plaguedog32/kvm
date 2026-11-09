//go:build linux

package guestbridge

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func testHIDPair(t *testing.T) (*HIDReports, *HIDReports) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	a, b := &HIDReports{fd: fds[0]}, &HIDReports{fd: fds[1]}
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b
}

func TestHIDReportAtomicTransferAndClose(t *testing.T) {
	a, b := testHIDPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	want := [ReportSize]byte{1, 2, 3, 4}
	if err := a.WriteReport(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := b.ReadReport(ctx)
	if err != nil || got != want {
		t.Fatalf("report: %v", err)
	}
	done := make(chan error, 1)
	go func() { _, err := b.ReadReport(ctx); done <- err }()
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("close left pending read blocked")
	}
	if err := b.Close(); err != nil {
		t.Fatal("second close", err)
	}
}

func TestHIDReadCancellationAndTruncatedReport(t *testing.T) {
	a, b := testHIDPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := b.ReadReport(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if _, err := unix.Write(a.fd, []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ReadReport(context.Background()); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal("truncated report accepted", err)
	}
}
