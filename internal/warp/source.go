package warp

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"sort"
	"strconv"
	"strings"
)

var documentationPrefix = netip.MustParsePrefix("2001:db8::/32")

// ErrNoSourcePrefix reports that no usable public IPv6 prefix could be found
// on the host. Fail closed: a private/ULA prefix would make WARP handshakes
// unroutable, and no prefix at all means every exit shares the host address.
var ErrNoSourcePrefix = errors.New("warp: no routable public IPv6 /64 prefix found")

// DiscoverSourcePrefix finds a public IPv6 prefix that is routed to this host
// and can be used as the outer source of a WARP tunnel. Addresses are drawn
// from it randomly, so a replacement exit presents a new source to Cloudflare
// and lands in a new egress /64.
//
// Candidates come from assigned interface addresses and the Linux IPv6 routing
// table. Link-local, unique-local,
// loopback and documentation addresses are rejected: those cannot be used as
// a source for a public WireGuard endpoint.
func DiscoverSourcePrefix() (netip.Prefix, error) {
	candidates := discoverCandidates()
	if len(candidates) == 0 {
		return netip.Prefix{}, ErrNoSourcePrefix
	}
	// Prefer a /64 (the quota scope and the usual routed block), then a
	// longer prefix over a very broad one, then a lower lexicographic value
	// so the choice is stable across restarts.
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.Bits() != b.Bits() {
			if a.Bits() == 64 {
				return true
			}
			if b.Bits() == 64 {
				return false
			}
			return a.Bits() > b.Bits()
		}
		return a.Addr().Compare(b.Addr()) < 0
	})
	return candidates[0], nil
}

func discoverCandidates() []netip.Prefix {
	seen := make(map[netip.Prefix]bool)
	out := make([]netip.Prefix, 0, 4)
	add := func(prefix netip.Prefix) {
		if seen[prefix] {
			return
		}
		seen[prefix] = true
		out = append(out, prefix)
	}

	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			addr, ok := netip.AddrFromSlice(ipNet.IP)
			if !ok {
				continue
			}
			bits, _ := ipNet.Mask.Size()
			if p, ok := candidatePrefix(addr.Unmap(), bits); ok {
				add(p)
			}
		}
	}

	if data, err := os.ReadFile("/proc/net/ipv6_route"); err == nil {
		scanner := bufio.NewScanner(strings.NewReader(string(data)))
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) < 3 {
				continue
			}
			raw, err := hex.DecodeString(fields[0])
			if err != nil || len(raw) != 16 {
				continue
			}
			addr, ok := netip.AddrFromSlice(raw)
			if !ok {
				continue
			}
			bits, err := strconv.ParseUint(fields[1], 16, 8)
			if err != nil {
				continue
			}
			if p, ok := candidatePrefix(addr, int(bits)); ok {
				add(p)
			}
		}
	}

	// `netstat -f inet6` is intentionally not used here: on macOS it lists
	// VPN routes such as ::1:0:0:0/68 whose base address is neither assigned
	// nor public. Interface addresses and the Linux routing table are enough.
	return out
}

// candidatePrefix normalizes one routed block into a /64 usable as source.
// Anything narrower than /64 has no host bits to randomize.
func candidatePrefix(addr netip.Addr, bits int) (netip.Prefix, bool) {
	addr = addr.Unmap()
	if bits < 48 || bits > 64 || !usableSourceAddr(addr) {
		return netip.Prefix{}, false
	}
	return netip.PrefixFrom(addr, 64).Masked(), true
}

func usableSourceAddr(addr netip.Addr) bool {
	if !addr.Is6() || !addr.IsGlobalUnicast() || addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() || addr.IsLoopback() ||
		addr.IsMulticast() || documentationPrefix.Contains(addr) {
		return false
	}
	// ::/96 (IPv4-compatible) and ::ffff:0:0/96 (IPv4-mapped) are not usable
	// public IPv6 sources, even though netip calls them global unicast.
	raw := addr.As16()
	if allZero(raw[:12]) || (allZero(raw[:10]) && raw[10] == 0xff && raw[11] == 0xff) {
		return false
	}
	return true
}

func allZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

// SourceError adds context to a failed auto-detection so an operator knows
// what to set manually.
func SourceError(err error) error {
	if !errors.Is(err, ErrNoSourcePrefix) {
		return err
	}
	return fmt.Errorf("%w; set WARP_SOURCE_IPV6 to a routed public IPv6 prefix (e.g. a /64 delegated to this host)", err)
}
