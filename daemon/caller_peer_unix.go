//go:build !windows

package main

import (
	"bufio"
	"net"
	"os"
	"strconv"
	"strings"
)

// readPpid reads /proc live, so no snapshot needs refreshing first.
func refreshProcessTreeForCaller() {}

// lookupPeerPIDPlatform finds the process owning the client end of a local
// TCP connection: the /proc/net/tcp row whose local address is the client and
// whose remote address is this server, then the process holding that socket.
// Hosts without /proc (macOS) report not found.
func lookupPeerPIDPlatform(client, server *net.TCPAddr) (int, bool) {
	// /proc/net/tcp is not a consistent snapshot: a row can be skipped while
	// other sockets open and close mid-read, so one miss gets a second read.
	for attempt := 0; attempt < 2; attempt++ {
		inode := uint64(0)
		for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
			if inode = findConnectionInode(path, client, server); inode != 0 {
				break
			}
		}
		if inode == 0 {
			continue
		}
		if pid := buildInodePidMap()[inode]; pid > 0 {
			return pid, true
		}
	}
	return 0, false
}

func findConnectionInode(path string, client, server *net.TCPAddr) uint64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue
		}
		fields := strings.Fields(sc.Text())
		if len(fields) < 10 {
			continue
		}
		// fields[1] = local, fields[2] = remote, fields[3] = state ("01" = ESTABLISHED)
		if fields[3] != "01" {
			continue
		}
		if !procNetAddrMatches(fields[1], client) || !procNetAddrMatches(fields[2], server) {
			continue
		}
		inode, _ := strconv.ParseUint(fields[9], 10, 64)
		if inode != 0 {
			return inode
		}
	}
	return 0
}

// procNetAddrMatches compares a /proc/net/tcp "ADDR:PORT" field with addr. An
// IPv4 peer of a dual-stack listener appears in tcp6 as ::ffff:a.b.c.d, so
// both spellings are tried.
func procNetAddrMatches(field string, addr *net.TCPAddr) bool {
	parts := strings.SplitN(field, ":", 2)
	if len(parts) != 2 {
		return false
	}
	port, err := strconv.ParseUint(parts[1], 16, 16)
	if err != nil || int(port) != addr.Port {
		return false
	}
	for _, want := range procNetHexForms(addr.IP) {
		if strings.EqualFold(parts[0], want) {
			return true
		}
	}
	return false
}

func procNetHexForms(ip net.IP) []string {
	if ip4 := ip.To4(); ip4 != nil {
		mapped := append(make([]byte, 0, 16), 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff)
		return []string{procNetHex(ip4), procNetHex(append(mapped, ip4...))}
	}
	if ip16 := ip.To16(); ip16 != nil {
		return []string{procNetHex(ip16)}
	}
	return nil
}

// procNetHex renders an address the way /proc/net/tcp does: each 32-bit word
// in host (little-endian) byte order, as upper-case hex.
func procNetHex(b []byte) string {
	const digits = "0123456789ABCDEF"
	out := make([]byte, 0, len(b)*2)
	for i := 0; i+3 < len(b); i += 4 {
		for j := 3; j >= 0; j-- {
			c := b[i+j]
			out = append(out, digits[c>>4], digits[c&0xf])
		}
	}
	return string(out)
}
