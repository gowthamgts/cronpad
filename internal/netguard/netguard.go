// Package netguard validates HTTP destinations and checks every resolved dial address.
package netguard

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Resolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}
type Policy struct {
	AllowPrivateNetworks bool
	Hosted               bool
	AllowedPorts         []int
	Resolver             Resolver
}

// DECISION: block translation/tunnel prefixes outright instead of decoding embedded IPv4.
var ranges = []netip.Prefix{netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("2001::/32"), netip.MustParsePrefix("::/96"), netip.MustParsePrefix("fec0::/10"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("2001:db8::/32")}

func (p Policy) CheckIP(ip netip.Addr) error {
	ip = ip.Unmap()
	if p.AllowPrivateNetworks {
		return nil
	}
	bad := !ip.IsValid() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified()
	for _, r := range ranges {
		bad = bad || r.Contains(ip)
	}
	if bad {
		return fmt.Errorf("destination address is not public")
	}
	return nil
}
func (p Policy) CheckURL(raw string) error {
	u, e := url.Parse(raw)
	if e != nil {
		return fmt.Errorf("URL: %w", e)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("invalid HTTP URL")
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	if e = p.checkPort(port); e != nil {
		return e
	}
	if ip, e := netip.ParseAddr(u.Hostname()); e == nil {
		return p.CheckIP(ip)
	}
	return nil
}
func (p Policy) checkPort(port string) error {
	n, e := strconv.Atoi(port)
	if e != nil || n < 1 || n > 65535 {
		return fmt.Errorf("invalid destination port")
	}
	if !p.Hosted || n == 80 || n == 443 {
		return nil
	}
	for _, v := range p.AllowedPorts {
		if n == v {
			return nil
		}
	}
	return fmt.Errorf("destination port is not allowed")
}

// DialContext resolves once, validates each address and dials that exact address,
// preventing a second lookup from bypassing checks through DNS rebinding.
func (p Policy) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, e := net.SplitHostPort(address)
	if e != nil {
		return nil, fmt.Errorf("destination: %w", e)
	}
	if e = p.checkPort(port); e != nil {
		return nil, e
	}
	resolver := p.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	ips, e := resolver.LookupIPAddr(ctx, strings.Trim(host, "[]"))
	if e != nil {
		return nil, fmt.Errorf("resolve: %w", e)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no destination addresses")
	}
	for _, ip := range ips {
		addr, ok := netip.AddrFromSlice(ip.IP)
		if !ok {
			return nil, fmt.Errorf("invalid resolved address")
		}
		if e = p.CheckIP(addr); e != nil {
			return nil, e
		}
	}
	d := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		h, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		ip, err := netip.ParseAddr(h)
		if err != nil {
			return err
		}
		return p.CheckIP(ip)
	}}
	for _, ip := range ips {
		var c net.Conn
		c, e = d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if e == nil {
			return c, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, fmt.Errorf("dial destination: %w", e)
}
