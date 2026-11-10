package localonly

import (
	"context"
	"net"
	"net/netip"
	"syscall"

	"github.com/pion/transport/v3"
	"github.com/pion/transport/v3/stdnet"
)

// ICE resolves browser mDNS candidates after parsing SDP. Enforce the final
// numeric packet destination too, so a public mDNS answer cannot escape.
type privateNet struct{ transport.Net }

func NewPrivateNet() (transport.Net, error) {
	base, err := stdnet.NewNet()
	if err != nil {
		return nil, err
	}
	return &privateNet{base}, nil
}

func packetAddressAllowed(addr net.Addr) bool {
	if addr == nil {
		return false
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return false
	}
	return PrivateIP(host)
}

type privatePacket struct {
	net.PacketConn
}

func (c privatePacket) SyscallConn() (syscall.RawConn, error) {
	return nil, ErrDisabled
}

type privateTCPListener struct{ transport.TCPListener }

func (l privateTCPListener) Accept() (net.Conn, error) {
	for {
		c, err := l.TCPListener.Accept()
		if err != nil {
			return nil, err
		}
		if packetAddressAllowed(c.RemoteAddr()) {
			return c, nil
		}
		c.Close()
	}
}
func (l privateTCPListener) AcceptTCP() (transport.TCPConn, error) {
	for {
		c, err := l.TCPListener.AcceptTCP()
		if err != nil {
			return nil, err
		}
		if packetAddressAllowed(c.RemoteAddr()) {
			return c, nil
		}
		c.Close()
	}
}

func (c privatePacket) WriteTo(b []byte, addr net.Addr) (int, error) {
	if !packetAddressAllowed(addr) {
		return 0, ErrDisabled
	}
	return c.PacketConn.WriteTo(b, addr)
}

type privateUDP struct {
	transport.UDPConn
}

// Link-local multicast listeners remain concrete UDP sockets for x/net's
// multicast reader. All wrappers deny raw controls to keep writes guarded.
func multicastSocket(addr net.Addr) bool {
	if addr == nil {
		return false
	}
	host, _, err := net.SplitHostPort(addr.String())
	ip, parseErr := netip.ParseAddr(host)
	return err == nil && parseErr == nil && ip.IsLinkLocalMulticast()
}

func (c privateUDP) SyscallConn() (syscall.RawConn, error) {
	return nil, ErrDisabled
}

func (c privateUDP) Write(b []byte) (int, error) {
	if !packetAddressAllowed(c.RemoteAddr()) {
		return 0, ErrDisabled
	}
	return c.UDPConn.Write(b)
}
func (c privateUDP) WriteTo(b []byte, addr net.Addr) (int, error) {
	if !packetAddressAllowed(addr) {
		return 0, ErrDisabled
	}
	return c.UDPConn.WriteTo(b, addr)
}
func (c privateUDP) WriteToUDP(b []byte, addr *net.UDPAddr) (int, error) {
	if addr == nil || !packetAddressAllowed(addr) {
		return 0, ErrDisabled
	}
	return c.UDPConn.WriteToUDP(b, addr)
}
func (c privateUDP) WriteMsgUDP(b, oob []byte, addr *net.UDPAddr) (int, int, error) {
	var target net.Addr = addr
	if addr == nil {
		target = c.RemoteAddr()
	}
	if !packetAddressAllowed(target) {
		return 0, 0, ErrDisabled
	}
	return c.UDPConn.WriteMsgUDP(b, oob, addr)
}

func (n *privateNet) ListenPacket(network, address string) (net.PacketConn, error) {
	c, err := n.Net.ListenPacket(network, address)
	if err != nil {
		return nil, err
	}
	return privatePacket{PacketConn: c}, nil
}
func (n *privateNet) ListenUDP(network string, addr *net.UDPAddr) (transport.UDPConn, error) {
	c, err := n.Net.ListenUDP(network, addr)
	if err != nil {
		return nil, err
	}
	// x/net's multicast ReadFrom requires a concrete *net.UDPConn. Wrapping
	// it produces immediate "invalid connection type" errors, spins mDNS's
	// read loop, and prevents Close from joining that loop. Only local
	// multicast listeners bypass the wrapper; ICE's query-only mDNS uses
	// these sockets for link-local multicast. Unicast ICE stays guarded.
	if multicastSocket(addr) {
		return c, nil
	}
	return privateUDP{UDPConn: c}, nil
}
func (n *privateNet) ListenTCP(network string, addr *net.TCPAddr) (transport.TCPListener, error) {
	c, err := n.Net.ListenTCP(network, addr)
	if err != nil {
		return nil, err
	}
	return privateTCPListener{c}, nil
}
func (n *privateNet) Dial(network, address string) (net.Conn, error) {
	return DialContext(context.Background(), network, address)
}
func (n *privateNet) DialUDP(network string, local, remote *net.UDPAddr) (transport.UDPConn, error) {
	if remote == nil || !PrivateIP(remote.IP.String()) {
		return nil, ErrDisabled
	}
	c, err := n.Net.DialUDP(network, local, remote)
	if err != nil {
		return nil, err
	}
	return privateUDP{UDPConn: c}, nil
}
func (n *privateNet) DialTCP(network string, local, remote *net.TCPAddr) (transport.TCPConn, error) {
	if remote == nil || !PrivateIP(remote.IP.String()) {
		return nil, ErrDisabled
	}
	return n.Net.DialTCP(network, local, remote)
}

type privateDialer struct{ net *privateNet }

func (d privateDialer) Dial(network, address string) (net.Conn, error) {
	return d.net.Dial(network, address)
}
func (n *privateNet) CreateDialer(_ *net.Dialer) transport.Dialer { return privateDialer{n} }

func (n *privateNet) ResolveIPAddr(network, address string) (*net.IPAddr, error) {
	if !LocalHost(address) {
		return nil, ErrDisabled
	}
	addr, err := n.Net.ResolveIPAddr(network, address)
	if err == nil && !PrivateIP(addr.IP.String()) {
		return nil, ErrDisabled
	}
	return addr, err
}
func (n *privateNet) ResolveUDPAddr(network, address string) (*net.UDPAddr, error) {
	host, _, err := net.SplitHostPort(address)
	ip, parseErr := netip.ParseAddr(host)
	if err != nil || (!LocalHost(host) && !(parseErr == nil && ip.IsLinkLocalMulticast())) {
		return nil, ErrDisabled
	}
	addr, err := n.Net.ResolveUDPAddr(network, address)
	if err == nil && !PrivateIP(addr.IP.String()) && !multicastSocket(addr) {
		return nil, ErrDisabled
	}
	return addr, err
}
func (n *privateNet) ResolveTCPAddr(network, address string) (*net.TCPAddr, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil || !LocalHost(host) {
		return nil, ErrDisabled
	}
	addr, err := n.Net.ResolveTCPAddr(network, address)
	if err == nil && !PrivateIP(addr.IP.String()) {
		return nil, ErrDisabled
	}
	return addr, err
}
