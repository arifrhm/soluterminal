package main

import (
	"net"
	"net/netip"
)

// WSL's Consomme port forwarding does not forward IPv4 loopback traffic to
// dual-stack IPv6 sockets. Go normally implements 0.0.0.0 with such a socket,
// so a successful bind can leave Code and local clients unable to connect.
// Honor an explicit IPv4 address on WSL; retain ordinary IPv6/hostname behavior.
func daemonListenerNetwork(addr string, insideWSL bool) string {
	if insideWSL {
		host, _, err := net.SplitHostPort(addr)
		if err == nil {
			ip, parseErr := netip.ParseAddr(host)
			if parseErr == nil && ip.Is4() {
				return "tcp4"
			}
		}
	}
	return "tcp"
}
