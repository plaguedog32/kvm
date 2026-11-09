package localonly

import (
	"context"
	"github.com/pion/ice/v4"
	"net"
	"testing"
	"time"
)

func TestPrivateTransportConnectsLocalICEPeers(t *testing.T) {
	makeAgent := func() *ice.Agent {
		network, err := NewPrivateNet()
		if err != nil {
			t.Fatal(err)
		}
		agent, err := ice.NewAgent(&ice.AgentConfig{Net: network, NetworkTypes: []ice.NetworkType{ice.NetworkTypeUDP4}, MulticastDNSMode: ice.MulticastDNSModeDisabled, IncludeLoopback: true, IPFilter: func(ip net.IP) bool { return ip.IsLoopback() }})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { agent.Close() })
		return agent
	}
	a, b := makeAgent(), makeAgent()
	aDone, bDone := make(chan struct{}), make(chan struct{})
	if err := a.OnCandidate(func(c ice.Candidate) {
		if c == nil {
			close(aDone)
			return
		}
		if err := b.AddRemoteCandidate(c); err != nil {
			t.Error(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := b.OnCandidate(func(c ice.Candidate) {
		if c == nil {
			close(bDone)
			return
		}
		if err := a.AddRemoteCandidate(c); err != nil {
			t.Error(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.GatherCandidates(); err != nil {
		t.Fatal(err)
	}
	if err := b.GatherCandidates(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, done := range []chan struct{}{aDone, bDone} {
		select {
		case <-done:
		case <-ctx.Done():
			t.Fatal("local candidate gathering timed out")
		}
	}
	au, ap, err := a.GetLocalUserCredentials()
	if err != nil {
		t.Fatal(err)
	}
	bu, bp, err := b.GetLocalUserCredentials()
	if err != nil {
		t.Fatal(err)
	}
	type accepted struct {
		conn *ice.Conn
		err  error
	}
	accept := make(chan accepted, 1)
	go func() { c, err := b.Accept(ctx, au, ap); accept <- accepted{c, err} }()
	outbound, err := a.Dial(ctx, bu, bp)
	if err != nil {
		t.Fatal(err)
	}
	peer := <-accept
	if peer.err != nil {
		t.Fatal(peer.err)
	}
	if _, err := outbound.Write([]byte("private ICE")); err != nil {
		t.Fatal(err)
	}
	received := make(chan string, 1)
	go func() {
		buf := make([]byte, 64)
		n, err := peer.conn.Read(buf)
		if err != nil {
			received <- err.Error()
			return
		}
		received <- string(buf[:n])
	}()
	select {
	case value := <-received:
		if value != "private ICE" {
			t.Fatal(value)
		}
	case <-ctx.Done():
		t.Fatal("local ICE transfer timed out")
	}
}
