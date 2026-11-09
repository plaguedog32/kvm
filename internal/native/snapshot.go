package native

import (
	"context"
	"fmt"
	"time"

	pb "github.com/jetkvm/kvm/internal/native/proto"
	"github.com/jetkvm/kvm/internal/videoimage"
)

type Snapshot struct {
	JPEG   []byte
	Width  int
	Height int
}

func (n *Native) VideoSnapshot(ctx context.Context) (*Snapshot, error) {
	raw, width, height, err := n.captureSnapshotFrame(ctx)
	if err != nil {
		return nil, err
	}
	// The frame is owned Go memory now; encoding does not need a hardware lock.
	data, err := videoimage.JPEG(ctx, raw, width, height)
	if err != nil {
		return nil, err
	}
	return &Snapshot{JPEG: data, Width: width, Height: height}, nil
}

func (n *Native) captureSnapshotFrame(ctx context.Context) ([]byte, int, int, error) {
	n.videoLock.Lock()
	defer n.videoLock.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, 0, 0, err
	}
	return videoSnapshot()
}

func (s *grpcServer) VideoSnapshot(ctx context.Context, _ *pb.Empty) (*pb.VideoSnapshotResponse, error) {
	frame, err := s.native.VideoSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return &pb.VideoSnapshotResponse{Jpeg: frame.JPEG, Width: int32(frame.Width), Height: int32(frame.Height)}, nil
}

func (c *GRPCClient) VideoSnapshot(ctx context.Context) (*Snapshot, error) {
	// Full-resolution JPEG encoding on the device can exceed five seconds
	// while virtual media is being transferred. Keep the capture bounded while
	// allowing normal operation under that load.
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	frame, err := c.client.VideoSnapshot(ctx, &pb.Empty{})
	if err != nil {
		return nil, err
	}
	return &Snapshot{JPEG: frame.Jpeg, Width: int(frame.Width), Height: int(frame.Height)}, nil
}

func (p *NativeProxy) VideoSnapshot(ctx context.Context) (*Snapshot, error) {
	p.clientMu.RLock()
	defer p.clientMu.RUnlock()
	if p.client == nil {
		return nil, fmt.Errorf("gRPC client not initialized")
	}
	return p.client.VideoSnapshot(ctx)
}

func (e *EmptyNativeInterface) VideoSnapshot(context.Context) (*Snapshot, error) {
	return nil, fmt.Errorf("screenshots unavailable in failsafe mode")
}
