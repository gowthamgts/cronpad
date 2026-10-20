package netguard

import (
	"context"
	"net"
	"net/netip"
	"testing"
)

type resolverFunc func(context.Context, string) ([]net.IPAddr, error)

func (f resolverFunc) LookupIPAddr(c context.Context, s string) ([]net.IPAddr, error) { return f(c, s) }
func TestBlocked(t *testing.T) {
	p := Policy{}
	for _, s := range []string{"127.0.0.1", "169.254.169.254", "::1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "100.64.0.1", "fc00::1", "fe80::1", "224.0.0.1", "ff02::1", "0.0.0.0", "::", "::ffff:127.0.0.1", "64:ff9b::a9fe:a9fe", "64:ff9b::a00:1", "64:ff9b:1::", "2002:a9fe:a9fe::1", "2002:7f00:1::1", "2001:0:4136:e378:8000:63bf:3fff:fdd2", "198.18.0.1", "192.0.0.8", "::a00:1", "fec0::1", "192.0.2.1", "198.51.100.1", "203.0.113.1", "2001:db8::1"} {
		t.Run(s, func(t *testing.T) {
			if p.CheckIP(netip.MustParseAddr(s)) == nil {
				t.Fatal("allowed", s)
			}
		})
	}
	if e := p.CheckIP(netip.MustParseAddr("8.8.8.8")); e != nil {
		t.Fatal(e)
	}
	p.AllowPrivateNetworks = true
	if e := p.CheckIP(netip.MustParseAddr("127.0.0.1")); e != nil {
		t.Fatal(e)
	}
}
func TestPrivateDNS(t *testing.T) {
	p := Policy{Resolver: resolverFunc(func(_ context.Context, name string) ([]net.IPAddr, error) {
		if name != "public.example" {
			t.Fatal(name)
		}
		return []net.IPAddr{{IP: net.ParseIP("10.2.3.4")}}, nil
	})}
	if _, e := p.DialContext(context.Background(), "tcp", "public.example:443"); e == nil {
		t.Fatal("private DNS address allowed")
	}
}
func TestPortsAndURLs(t *testing.T) {
	p := Policy{Hosted: true}
	for _, s := range []string{"http://example.org:8080", "file:///etc/passwd", "http://user:pass@example.org", "https://[::1]"} {
		if p.CheckURL(s) == nil {
			t.Fatal(s)
		}
	}
	for _, s := range []string{"https://example.org", "http://example.org"} {
		if e := p.CheckURL(s); e != nil {
			t.Fatal(e)
		}
	}
	p.AllowedPorts = []int{8443}
	if e := p.CheckURL("https://example.org:8443"); e != nil {
		t.Fatal(e)
	}
}
