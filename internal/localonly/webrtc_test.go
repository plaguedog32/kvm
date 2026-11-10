package localonly

import (
	"errors"
	"net"
	"testing"
	"time"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

func TestResolvedPublicICEDestinationNeverSendsPacket(t *testing.T) {
	// A nil underlying connection would panic if the policy forwarded a write.
	c := privateUDP{}
	external := &net.UDPAddr{IP: net.ParseIP("8.8.8.8"), Port: 50000}
	if _, err := c.WriteTo([]byte("ICE"), external); err != ErrDisabled {
		t.Fatal(err)
	}
	if _, err := c.WriteToUDP([]byte("ICE"), external); err != ErrDisabled {
		t.Fatal(err)
	}
	if _, _, err := c.WriteMsgUDP([]byte("ICE"), nil, external); err != ErrDisabled {
		t.Fatal(err)
	}
	if _, err := c.WriteToUDP([]byte("ICE"), &net.UDPAddr{IP: net.ParseIP("224.0.0.251"), Port: 5353}); err != ErrDisabled {
		t.Fatal("unicast ICE socket sent to a multicast destination", err)
	}
}

func TestOnlyLinkLocalMulticastUDPListenersBypassWrapper(t *testing.T) {
	n, err := NewPrivateNet()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ network, ip string }{
		{"udp4", ""}, {"udp4", "127.0.0.1"}, {"udp4", "239.255.255.250"},
		{"udp6", ""}, {"udp6", "::1"}, {"udp6", "ff05::c"},
	} {
		socket, err := n.ListenUDP(tc.network, &net.UDPAddr{IP: net.ParseIP(tc.ip)})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := socket.(privateUDP); !ok {
			t.Errorf("%s %q bypassed guard", tc.network, tc.ip)
		}
		socket.Close()
	}
}

func TestPrivateNetworkRetainsLocalMDNSResolutionAndSocketOptions(t *testing.T) {
	n, err := NewPrivateNet()
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"224.0.0.251:5353", "[ff02::fb]:5353"} {
		if _, err := n.ResolveUDPAddr("udp", address); err != nil {
			t.Fatal("local mDNS address was rejected", err)
		}
	}
	socket, err := n.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("224.0.0.251")})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	if err := ipv4.NewPacketConn(socket).SetMulticastLoopback(true); err != nil {
		t.Fatal("multicast socket controls were lost", err)
	}
}

func TestPrivateUnicastSocketDoesNotExposeRawWrites(t *testing.T) {
	n, err := NewPrivateNet()
	if err != nil {
		t.Fatal(err)
	}
	socket, err := n.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	if _, err := socket.(privateUDP).SyscallConn(); err != ErrDisabled {
		t.Fatalf("unicast raw socket access: %v", err)
	}
	packet, err := n.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer packet.Close()
	if _, err := packet.(privatePacket).SyscallConn(); err != ErrDisabled {
		t.Fatalf("unicast raw packet access: %v", err)
	}
}

func TestMDNSReadBlocksAndCloseUnblocksForBothFamilies(t *testing.T) {
	for _, tc := range []struct{ network, address string }{
		{"udp4", "224.0.0.251:0"}, {"udp6", "[ff02::fb]:0"},
	} {
		t.Run(tc.network, func(t *testing.T) {
			n, err := NewPrivateNet()
			if err != nil {
				t.Fatal(err)
			}
			addr, err := net.ResolveUDPAddr(tc.network, tc.address)
			if err != nil {
				t.Fatal(err)
			}
			socket, err := n.ListenUDP(tc.network, addr)
			if err != nil {
				t.Fatal(err)
			}
			defer socket.Close()
			var read func([]byte) error
			if tc.network == "udp4" {
				pc := ipv4.NewPacketConn(socket)
				read = func(b []byte) error { _, _, _, err := pc.ReadFrom(b); return err }
			} else {
				pc := ipv6.NewPacketConn(socket)
				read = func(b []byte) error { _, _, _, err := pc.ReadFrom(b); return err }
			}
			result := make(chan error, 1)
			go func() { result <- read(make([]byte, 128)) }()
			select {
			case err := <-result:
				t.Fatalf("idle multicast read returned immediately: %v", err)
			case <-time.After(30 * time.Millisecond):
			}
			socket.Close()
			select {
			case err := <-result:
				if !errors.Is(err, net.ErrClosed) {
					t.Fatalf("close returned %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("multicast read did not stop after close")
			}
		})
	}
}
func TestPrivateICENetworkStillTransfersUDP(t *testing.T) {
	n, err := NewPrivateNet()
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	sender, err := n.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	if _, err = sender.WriteToUDP([]byte("local"), receiver.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatal(err)
	}
	receiver.SetReadDeadline(time.Now().Add(time.Second))
	b := make([]byte, 16)
	count, _, err := receiver.ReadFromUDP(b)
	if err != nil || string(b[:count]) != "local" {
		t.Fatal("local ICE packet failed", err)
	}
	if _, err = n.DialUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP("8.8.8.8"), Port: 123}); err != ErrDisabled {
		t.Fatal("external UDP dial was accepted", err)
	}
}
