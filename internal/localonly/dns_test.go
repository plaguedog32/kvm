package localonly

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"testing"
	"time"
)

func TestLocalHostnameResolvingToPublicAddressIsBlocked(t *testing.T) {
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	go func() {
		for {
			b := make([]byte, 1024)
			count, addr, err := server.ReadFromUDP(b)
			if err != nil {
				return
			}
			b = b[:count]
			if len(b) < 17 {
				continue
			}
			end := 12
			for end < len(b) && b[end] != 0 {
				end += int(b[end]) + 1
			}
			end++
			if end+4 > len(b) {
				continue
			}
			aQuery := binary.BigEndian.Uint16(b[end:end+2]) == 1
			reply := make([]byte, 12)
			copy(reply, b[:2])
			reply[2] = 0x81
			reply[3] = 0x80
			reply[5] = 1
			if aQuery {
				reply[7] = 1
			}
			reply = append(reply, b[12:end+4]...)
			if aQuery {
				reply = append(reply, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 1, 0, 4, 203, 0, 113, 10)
			}
			server.WriteToUDP(reply, addr)
		}
	}()
	original := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "udp", server.LocalAddr().String())
	}}
	defer func() { net.DefaultResolver = original }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := DialContext(ctx, "tcp", "rebind.local:443"); !errors.Is(err, ErrDisabled) {
		t.Fatal("public DNS result was not rejected", err)
	}
}
