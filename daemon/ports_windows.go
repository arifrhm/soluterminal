//go:build windows

package main

// Windows port registry. Enumerates TCP listening sockets via
// GetExtendedTcpTable (iphlpapi.dll) for both IPv4 and IPv6, joins
// to processes via the process-tree snapshot in procfs_windows.go,
// then tags ports with the pane that owns them (if any). Parallels
// ports_unix.go's /proc/net/tcp + /proc/*/fd walk.
//
// What we don't do vs Unix:
//   - readCmdline / readProcCwd. Reading another process's command
//     line requires PROCESS_QUERY_LIMITED_INFORMATION + NtQueryProcess
//     (undocumented) or PROCESS_VM_READ to dig the PEB. Live cwd is
//     similarly hairy. Leave both empty for now; the UI copes.

import (
	"encoding/binary"
	"strconv"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	iphlpapi                = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetExtendedTcpTable = iphlpapi.NewProc("GetExtendedTcpTable")
)

// TCP_TABLE_CLASS enum values we care about. See MIB_TCP_TABLE_CLASS.
const (
	tcpTableOwnerPidListener = 3
	tcpTableOwnerPidAll      = 5
)

// AF_* values for GetExtendedTcpTable.
const (
	afInet  = 2
	afInet6 = 23
)

// MIB_TCP_STATE values. Only LISTEN matters for us; but we also get
// TCP_TABLE_OWNER_PID_LISTENER which already filters to listeners.
const mibTcpStateListen = 2

// MIB_TCPROW_OWNER_PID — IPv4 listener row. Matches <tcpmib.h>.
type mibTcpRowOwnerPid struct {
	State      uint32
	LocalAddr  uint32 // network byte order
	LocalPort  uint32 // only the low 16 bits are the port (network byte order)
	RemoteAddr uint32
	RemotePort uint32
	OwningPid  uint32
}

// MIB_TCP6ROW_OWNER_PID — IPv6 listener row.
type mibTcp6RowOwnerPid struct {
	LocalAddr     [16]byte
	LocalScopeId  uint32
	LocalPort     uint32
	RemoteAddr    [16]byte
	RemoteScopeId uint32
	RemotePort    uint32
	State         uint32
	OwningPid     uint32
}

type tcpEntry struct {
	proto string
	addr  string
	port  uint16
	pid   int
}

// getExtendedTcpTable fetches the IPv4/IPv6 listener table.
// Returns the raw buffer sized to the kernel's current view; callers
// interpret it as MIB_TCPTABLE_OWNER_PID / MIB_TCP6TABLE_OWNER_PID.
func getExtendedTcpTable(af uint32) ([]byte, error) {
	return getExtendedTcpTableClass(af, tcpTableOwnerPidListener)
}

// getExtendedTcpTableClass fetches the table of the given TCP_TABLE_CLASS.
func getExtendedTcpTableClass(af, class uint32) ([]byte, error) {
	// First call with size=0 to get the required buffer size.
	var size uint32
	_, _, _ = procGetExtendedTcpTable.Call(
		0, uintptr(unsafe.Pointer(&size)), 0,
		uintptr(af), uintptr(class), 0,
	)
	if size == 0 {
		return nil, nil
	}
	// Retry until buffer is large enough. The table can grow between
	// our size probe and the real call, so loop until it fits.
	for {
		buf := make([]byte, size)
		ret, _, _ := procGetExtendedTcpTable.Call(
			uintptr(unsafe.Pointer(&buf[0])),
			uintptr(unsafe.Pointer(&size)),
			0, // bOrder = FALSE (we don't care about sort)
			uintptr(af),
			uintptr(class),
			0,
		)
		switch ret {
		case 0: // NO_ERROR
			return buf[:size], nil
		case 122: // ERROR_INSUFFICIENT_BUFFER — grew; retry
			continue
		default:
			return nil, syscall.Errno(ret)
		}
	}
}

func parseTcp4Listeners(buf []byte) []tcpEntry {
	if len(buf) < 4 {
		return nil
	}
	n := binary.LittleEndian.Uint32(buf[:4])
	rows := buf[4:]
	rowSize := int(unsafe.Sizeof(mibTcpRowOwnerPid{}))
	var out []tcpEntry
	for i := 0; i < int(n); i++ {
		start := i * rowSize
		if start+rowSize > len(rows) {
			break
		}
		r := *(*mibTcpRowOwnerPid)(unsafe.Pointer(&rows[start]))
		if r.State != mibTcpStateListen {
			continue
		}
		// LocalAddr is in network byte order (big-endian when viewed
		// as 4 bytes). On x86/x64 we read it as little-endian uint32,
		// so the raw bytes are already in the right order when we
		// extract per-byte.
		addr := ipv4Format(r.LocalAddr)
		port := uint16(r.LocalPort&0xff)<<8 | uint16((r.LocalPort>>8)&0xff)
		out = append(out, tcpEntry{
			proto: "tcp",
			addr:  addr,
			port:  port,
			pid:   int(r.OwningPid),
		})
	}
	return out
}

func parseTcp6Listeners(buf []byte) []tcpEntry {
	if len(buf) < 4 {
		return nil
	}
	n := binary.LittleEndian.Uint32(buf[:4])
	// Table is 4-byte count + rows. But ROW itself is aligned —
	// MIB_TCP6TABLE_OWNER_PID has a DWORD dwNumEntries followed by
	// MIB_TCP6ROW_OWNER_PID[] which has no padding issues because
	// UCHAR[16] aligns fine.
	rows := buf[4:]
	rowSize := int(unsafe.Sizeof(mibTcp6RowOwnerPid{}))
	var out []tcpEntry
	for i := 0; i < int(n); i++ {
		start := i * rowSize
		if start+rowSize > len(rows) {
			break
		}
		r := *(*mibTcp6RowOwnerPid)(unsafe.Pointer(&rows[start]))
		if r.State != mibTcpStateListen {
			continue
		}
		port := uint16(r.LocalPort&0xff)<<8 | uint16((r.LocalPort>>8)&0xff)
		out = append(out, tcpEntry{
			proto: "tcp6",
			addr:  ipv6Format(r.LocalAddr[:]),
			port:  port,
			pid:   int(r.OwningPid),
		})
	}
	return out
}

// ipv4Format turns a DWORD holding a network-byte-order IPv4 address
// into dotted-quad. The DWORD is stored little-endian on x86/x64, so
// byte 0 is the MSB of the network representation (= first octet).
func ipv4Format(dw uint32) string {
	b0 := byte(dw)
	b1 := byte(dw >> 8)
	b2 := byte(dw >> 16)
	b3 := byte(dw >> 24)
	return strconv.Itoa(int(b0)) + "." +
		strconv.Itoa(int(b1)) + "." +
		strconv.Itoa(int(b2)) + "." +
		strconv.Itoa(int(b3))
}

// ipv6Format renders the WSL-style :: when the address is all zeros
// (dual-stack ANY) and otherwise produces a simple hex:hex:... form.
// We don't bother with RFC-5952 :: compression — the UI is fine with
// verbose forms for this panel.
func ipv6Format(b []byte) string {
	allZero := true
	for _, c := range b {
		if c != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return "::"
	}
	// v4-mapped: ::ffff:x.x.x.x
	if b[10] == 0xff && b[11] == 0xff {
		zero := true
		for _, c := range b[:10] {
			if c != 0 {
				zero = false
				break
			}
		}
		if zero {
			return strconv.Itoa(int(b[12])) + "." +
				strconv.Itoa(int(b[13])) + "." +
				strconv.Itoa(int(b[14])) + "." +
				strconv.Itoa(int(b[15]))
		}
	}
	// Plain hex groups.
	const hex = "0123456789abcdef"
	out := make([]byte, 0, 40)
	for i := 0; i < 16; i += 2 {
		if i > 0 {
			out = append(out, ':')
		}
		out = append(out, hex[b[i]>>4], hex[b[i]&0xf], hex[b[i+1]>>4], hex[b[i+1]&0xf])
	}
	return string(out)
}

// scan implements the PortRegistry periodic tick. Same shape as
// ports_unix.go.
func (pr *PortRegistry) scan() {
	pr.scanMu.Lock()
	defer pr.scanMu.Unlock()
	buf4, err4 := getExtendedTcpTable(afInet)
	buf6, err6 := getExtendedTcpTable(afInet6)
	if err4 != nil && err6 != nil {
		pr.invalidate("Windows service discovery is unavailable.")
		return
	}
	if err4 != nil || err6 != nil {
		pr.setWarning("Some Windows listeners could not be read. The service list may be incomplete.")
	} else {
		pr.setWarning("")
	}
	all := append(parseTcp4Listeners(buf4), parseTcp6Listeners(buf6)...)

	// Build pane-pid map so we can tag ports with their owning pane.
	paneByPid := map[int]string{}
	for _, p := range pr.mgr.List() {
		for _, pid := range p.ChildPids() {
			paneByPid[pid] = p.ID
		}
	}

	selfPID := windows.GetCurrentProcessId()
	selfSID, _ := currentUserSID()
	if selfSID == nil {
		pr.invalidate("Service discovery could not verify the current Windows user.")
		return
	}

	previous := pr.snapshot()

	fresh := map[string]*Port{}
	for _, e := range all {
		if e.pid == 0 {
			continue
		}
		if uint32(e.pid) == selfPID {
			// Don't show the daemon's own listener (:8787 / :18787).
			continue
		}
		// Windows equivalent of the Unix UID filter: show only ports
		// owned by the current user. Without this, the panel is
		// flooded by RPC, SMB, Windows Push, etc. If we can't read a
		// pid's token (access denied from a system process, etc.)
		// treat as "not us" and skip — matches the spirit of the
		// Unix filter, which also skips pids it can't read.
		if selfSID == nil || !processOwnedByCurrentUser(e.pid, selfSID) {
			continue
		}

		paneID := paneByPid[e.pid]
		key := e.proto + "|" + e.addr + "|" + strconv.Itoa(int(e.port))
		createdAt := processCreationTime(e.pid)
		if createdAt <= 0 {
			continue
		}
		identity := strconv.FormatInt(createdAt, 10)
		if existing, had := previous[key]; had && samePortListener(existing, e.pid, identity) {
			// Copy, don't mutate — commitLocked diffs against pr.seen (see
			// ports_unix.go for the full note).
			cp := *existing
			cp.PaneID = paneID
			cp.Pid = e.pid
			cp.Process = processName(e.pid)
			fresh[key] = &cp
			continue
		}
		fresh[key] = &Port{
			Port:     e.port,
			Addr:     e.addr,
			Pid:      e.pid,
			Process:  processName(e.pid),
			Cmdline:  "", // see file-level comment — deliberately left empty on Windows
			Cwd:      "",
			PaneID:   paneID,
			FirstAt:  time.Now().UTC(),
			Proto:    e.proto,
			identity: identity,
		}
	}
	pr.commitScan(fresh)
}

// readComm is called from pane.go's HasClaudeDescendant. Returns the
// process basename (without .exe), matching /proc/<pid>/comm on Unix.
func readComm(pid int) string {
	return processName(pid)
}

// currentUserSID returns the SID of the user running the daemon.
// Computed once per call but caller caches per-scan to avoid repeating
// the syscall on every port. Using TokenUser on the process token is
// the standard "who am I" — matches the SID Windows sets as the
// token user for child processes we care about.
func currentUserSID() (*windows.SID, error) {
	tok := windows.GetCurrentProcessToken()
	tu, err := tok.GetTokenUser()
	if err != nil {
		return nil, err
	}
	// Return a copy — the TOKEN_USER buffer is owned by the caller
	// and we'd rather keep our own managed SID pointer. windows.SID
	// is an opaque handle; Copy makes one on Go-managed memory.
	return tu.User.Sid.Copy()
}

// processOwnedByCurrentUser returns true iff the token user of pid
// matches selfSID. Opens with PROCESS_QUERY_LIMITED_INFORMATION which
// is the cheapest access that still lets us read the token.
// A false return covers both "owned by another user" and "can't tell"
// — the latter is what happens for system processes we lack rights on.
func processOwnedByCurrentUser(pid int, selfSID *windows.SID) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)

	var tok windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &tok); err != nil {
		return false
	}
	defer tok.Close()

	tu, err := tok.GetTokenUser()
	if err != nil {
		return false
	}
	return windows.EqualSid(tu.User.Sid, selfSID)
}
