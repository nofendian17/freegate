package warp

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

const (
	probeTimeout    = 5 * time.Second
	probeCandidates = 10
)

// Stack selects the address family used inside the tunnel.
type Stack string

const (
	StackIPv4 Stack = "ipv4"
	StackIPv6 Stack = "ipv6"
	StackDual Stack = "dual"

	// DefaultStack is dual: IPv6-capable upstreams (OpenCode, LLM7) keep the
	// per-tunnel IPv6 egress and its /64 quota scope, while IPv4-only hosts
	// (for example api.kilo.ai) remain reachable. An IPv4-only stack is not a
	// good default because Cloudflare's public IPv4 egress is shared by many
	// users and its free quota is usually already spent.
	DefaultStack = StackDual
)

var (
	dnsV4 = netip.MustParseAddr("1.1.1.1")
	dnsV6 = netip.MustParseAddr("2606:4700:4700::1111")
)

// ParseStack normalizes an address-family selection.
func ParseStack(value string) (Stack, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return DefaultStack, nil
	case "ipv6", "v6":
		return StackIPv6, nil
	case "ipv4", "v4":
		return StackIPv4, nil
	case "dual", "both":
		return StackDual, nil
	default:
		return "", fmt.Errorf("unsupported WARP stack %q; use ipv6, ipv4 or dual", value)
	}
}

type exit struct {
	id        string
	egress    netip.Addr
	stack     Stack
	device    *device.Device
	network   *netstack.Net
	mu        sync.Mutex
	refs      int
	retired   bool
	closed    bool
	penalized bool
	closeOnce sync.Once
}

// newExit builds one userspace tunnel for reg. A valid source pins the outer
// IPv6 socket; IPv4 endpoints remain available as a fallback when the host
// cannot reach Cloudflare over IPv6.
func newExit(ctx context.Context, reg registration, stack Stack, source netip.Addr) (*exit, error) {
	normalized, err := ParseStack(string(stack))
	if err != nil {
		return nil, err
	}
	stack = normalized
	localV6, v6Err := parseAddress(reg.AddressV6)
	localV4, v4Err := parseAddress(reg.AddressV4)
	var localAddresses []netip.Addr
	var dns []netip.Addr
	switch stack {
	case StackIPv6:
		if v6Err != nil || !localV6.Is6() {
			return nil, fmt.Errorf("invalid WARP IPv6 address %q", reg.AddressV6)
		}
		localAddresses = []netip.Addr{localV6}
		dns = []netip.Addr{dnsV6}
	case StackIPv4:
		if v4Err != nil || !localV4.Is4() {
			return nil, fmt.Errorf("invalid WARP IPv4 address %q", reg.AddressV4)
		}
		localAddresses = []netip.Addr{localV4}
		dns = []netip.Addr{dnsV4}
	case StackDual:
		if v6Err == nil && localV6.Is6() {
			localAddresses = append(localAddresses, localV6)
		}
		if v4Err == nil && localV4.Is4() {
			localAddresses = append(localAddresses, localV4)
		}
		if len(localAddresses) == 0 {
			return nil, fmt.Errorf("WARP registration has no usable address: v4=%q v6=%q", reg.AddressV4, reg.AddressV6)
		}
		dns = []netip.Addr{dnsV4, dnsV6}
	}
	tunDevice, network, err := netstack.CreateNetTUN(localAddresses, dns, 1280)
	if err != nil {
		return nil, fmt.Errorf("create WARP network stack: %w", err)
	}
	privateKey, err := uapiKey(reg.PrivateKey)
	if err != nil {
		_ = tunDevice.Close()
		return nil, fmt.Errorf("decode WARP private key: %w", err)
	}
	peerKey, err := uapiKey(reg.PeerPublicKey)
	if err != nil {
		_ = tunDevice.Close()
		return nil, fmt.Errorf("decode WARP peer key: %w", err)
	}
	endpoints, err := resolveEndpoints(ctx, reg.Endpoints, reg.Endpoint)
	if err != nil {
		_ = tunDevice.Close()
		return nil, err
	}
	if source.IsValid() {
		endpoints = preferIPv6Endpoints(endpoints)
	} else {
		// No pinned source: try IPv4 endpoints first. On hosts without a
		// public IPv6 route, trying IPv6 first produces noisy "no route to
		// host" handshake errors and delays the working IPv4 fallback.
		endpoints = preferIPv4Endpoints(endpoints)
	}
	wg := device.NewDevice(tunDevice, &simpleBind{source: source}, device.NewLogger(device.LogLevelError, "warp: "))
	uapi := fmt.Sprintf(
		"private_key=%s\npublic_key=%s\nallowed_ip=0.0.0.0/0\nallowed_ip=::/0\nendpoint=%s\npersistent_keepalive_interval=25\n",
		privateKey, peerKey, endpoints[0],
	)
	if err := wg.IpcSet(uapi); err != nil {
		wg.Close()
		<-wg.Wait()
		return nil, fmt.Errorf("configure WARP device: %w", err)
	}
	if err := wg.Up(); err != nil {
		wg.Close()
		<-wg.Wait()
		return nil, fmt.Errorf("start WARP device: %w", err)
	}
	e := &exit{id: reg.ID, stack: stack, device: wg, network: network}
	egress, err := e.probe(ctx, wg, peerKey, endpoints)
	if err != nil {
		e.close()
		return nil, err
	}
	e.egress = egress
	return e, nil
}

// preferIPv6Endpoints keeps IPv6 endpoints first so the rotating outer source
// is used, but retains IPv4 fallbacks for hosts without IPv6 reachability.
func preferIPv6Endpoints(endpoints []string) []string {
	ordered := make([]string, 0, len(endpoints))
	ipv4 := make([]string, 0, len(endpoints))
	for _, endpoint := range endpoints {
		host, _, err := net.SplitHostPort(endpoint)
		if err != nil {
			continue
		}
		addr, err := netip.ParseAddr(host)
		if err != nil || addr.Is4In6() {
			continue
		}
		if addr.Is6() {
			ordered = append(ordered, endpoint)
		} else {
			ipv4 = append(ipv4, endpoint)
		}
	}
	return append(ordered, ipv4...)
}

// preferIPv4Endpoints keeps IPv4 endpoints first and IPv6 ones after them,
// retaining both families so a tunnel still comes up if IPv4 is unusable.
func preferIPv4Endpoints(endpoints []string) []string {
	ordered := make([]string, 0, len(endpoints))
	ipv6 := make([]string, 0, len(endpoints))
	for _, endpoint := range endpoints {
		host, _, err := net.SplitHostPort(endpoint)
		if err != nil {
			continue
		}
		addr, err := netip.ParseAddr(host)
		if err != nil || addr.Is4In6() {
			continue
		}
		if addr.Is4() {
			ordered = append(ordered, endpoint)
		} else if addr.Is6() {
			ipv6 = append(ipv6, endpoint)
		}
	}
	return append(ordered, ipv6...)
}

func resolveEndpoints(ctx context.Context, candidates []string, fallback string) ([]string, error) {
	if len(candidates) == 0 && fallback != "" {
		candidates = []string{fallback}
	}
	seen := make(map[string]bool)
	var endpoints []string
	for _, candidate := range candidates {
		normalized, err := normalizeEndpoint(candidate)
		if err != nil {
			continue
		}
		host, port, _ := net.SplitHostPort(normalized)
		if net.ParseIP(host) != nil {
			if !seen[normalized] {
				seen[normalized] = true
				endpoints = append(endpoints, normalized)
			}
		} else {
			addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				continue
			}
			for _, ip := range addrs {
				value := net.JoinHostPort(ip.IP.String(), port)
				if !seen[value] {
					seen[value] = true
					endpoints = append(endpoints, value)
				}
			}
		}
		if len(endpoints) >= probeCandidates {
			break
		}
	}
	if len(endpoints) == 0 {
		return nil, fmt.Errorf("resolve WARP endpoint candidates %v", candidates)
	}
	return endpoints[:min(len(endpoints), probeCandidates)], nil
}

func normalizeEndpoint(endpoint string) (string, error) {
	endpoint = strings.TrimSpace(endpoint)
	if !strings.Contains(endpoint, ":") {
		endpoint = net.JoinHostPort(endpoint, "2408")
	} else if addr, err := netip.ParseAddr(endpoint); err == nil {
		endpoint = net.JoinHostPort(addr.String(), "2408")
	}
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return "", fmt.Errorf("invalid WARP endpoint %q", endpoint)
	}
	if port == "" || port == "0" {
		port = "2408"
	}
	return net.JoinHostPort(host, port), nil
}

func parseAddress(value string) (netip.Addr, error) {
	value = strings.TrimSpace(value)
	if addr, err := netip.ParseAddr(value); err == nil {
		return addr.Unmap(), nil
	}
	prefix, err := netip.ParsePrefix(value)
	if err != nil {
		return netip.Addr{}, err
	}
	return prefix.Addr().Unmap(), nil
}

func uapiKey(encoded string) (string, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil || len(key) != 32 {
		return "", errors.New("invalid WireGuard key")
	}
	return hex.EncodeToString(key), nil
}

type probeTarget struct {
	network string
	url     string
}

func probeTargets(stack Stack) []probeTarget {
	switch stack {
	case StackIPv4:
		return []probeTarget{{network: "tcp4", url: "https://1.1.1.1/cdn-cgi/trace"}}
	case StackDual:
		// Probe IPv6 first: a dual-stack tunnel should report its /64-scoped
		// IPv6 egress, which is the quota identity for IPv6 upstreams. The
		// IPv4 trace stays as a fallback for hosts where WARP IPv6 is down.
		return []probeTarget{
			{network: "tcp6", url: "https://[2606:4700:4700::1111]/cdn-cgi/trace"},
			{network: "tcp4", url: "https://1.1.1.1/cdn-cgi/trace"},
		}
	default:
		return []probeTarget{{network: "tcp6", url: "https://[2606:4700:4700::1111]/cdn-cgi/trace"}}
	}
}

func (e *exit) probe(ctx context.Context, wg *device.Device, peerKey string, endpoints []string) (netip.Addr, error) {
	var lastErr error
	for index, endpoint := range endpoints {
		if index > 0 {
			if err := wg.IpcSet(fmt.Sprintf("public_key=%s\nendpoint=%s\n", peerKey, endpoint)); err != nil {
				lastErr = fmt.Errorf("switch WARP endpoint to %s: %w", endpoint, err)
				continue
			}
		}
		for _, target := range probeTargets(e.stack) {
			egress, err := e.probeTarget(ctx, target)
			if err == nil {
				return egress, nil
			}
			lastErr = err
		}
	}
	return netip.Addr{}, fmt.Errorf("verify WARP %s egress: %w", e.stack, lastErr)
}

func (e *exit) probeTarget(ctx context.Context, target probeTarget) (netip.Addr, error) {
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	transport := &http.Transport{
		Proxy:             nil,
		DisableKeepAlives: true,
		TLSClientConfig:   &tls.Config{NextProtos: []string{"http/1.1"}},
		TLSNextProto:      make(map[string]func(string, *tls.Conn) http.RoundTripper),
		DialContext: func(ctx context.Context, _, address string) (net.Conn, error) {
			return e.network.DialContext(ctx, target.network, address)
		},
	}
	defer transport.CloseIdleConnections()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, target.url, nil)
	if err != nil {
		return netip.Addr{}, err
	}
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		return netip.Addr{}, err
	}
	defer resp.Body.Close()
	egress, err := parseTrace(resp.Body)
	if err != nil {
		return netip.Addr{}, err
	}
	if target.network == "tcp4" && !egress.Is4() {
		return netip.Addr{}, fmt.Errorf("WARP trace over IPv4 reported %s", egress)
	}
	if target.network == "tcp6" && !egress.Is6() {
		return netip.Addr{}, fmt.Errorf("WARP trace over IPv6 reported %s", egress)
	}
	return egress, nil
}

func parseTrace(body io.Reader) (netip.Addr, error) {
	scanner := bufio.NewScanner(io.LimitReader(body, 64<<10))
	var rawIP string
	warpEnabled := false
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "ip":
			rawIP = strings.TrimSpace(value)
		case "warp":
			warpEnabled = strings.TrimSpace(value) == "on"
		}
	}
	if err := scanner.Err(); err != nil {
		return netip.Addr{}, err
	}
	if !warpEnabled {
		return netip.Addr{}, errors.New("Cloudflare trace reports warp=off")
	}
	addr, err := netip.ParseAddr(rawIP)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("invalid trace IP %q", rawIP)
	}
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() {
		return netip.Addr{}, fmt.Errorf("trace returned non-public address %s", addr)
	}
	return addr, nil
}

func (e *exit) addRef() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.retired || e.closed {
		return false
	}
	e.refs++
	return true
}

func (e *exit) release() {
	e.mu.Lock()
	if e.refs > 0 {
		e.refs--
	}
	closeNow := e.refs == 0 && e.retired && !e.closed
	e.mu.Unlock()
	if closeNow {
		e.close()
	}
}

func (e *exit) retire() {
	e.mu.Lock()
	e.retired = true
	closeNow := e.refs == 0 && !e.closed
	e.mu.Unlock()
	if closeNow {
		e.close()
	}
}

func (e *exit) close() {
	e.closeOnce.Do(func() {
		e.mu.Lock()
		e.closed = true
		e.mu.Unlock()
		if e.device != nil {
			e.device.Close()
			<-e.device.Wait()
		}
	})
}

func (e *exit) isPenalized() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.penalized
}

func (e *exit) markPenalized() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.penalized {
		return false
	}
	e.penalized = true
	return true
}

func (e *exit) clearPenalty() {
	e.mu.Lock()
	e.penalized = false
	e.mu.Unlock()
}

func (e *exit) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if e.network == nil {
		return nil, errors.New("WARP network is closed")
	}
	network = strings.ToLower(strings.TrimSpace(network))
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		return nil, fmt.Errorf("WARP supports TCP only, got %q", network)
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("WARP dial address %q: %w", address, err)
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return e.dialAddr(ctx, network, ip, port)
	}
	addrs, err := e.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for _, ip := range addrs {
		conn, derr := e.dialAddr(ctx, network, ip, port)
		if derr == nil {
			return conn, nil
		}
		lastErr = derr
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("WARP %s stack has no usable address for %s", e.stack, host)
	}
	return nil, lastErr
}

// dialAddr connects to one literal address inside the tunnel. The stack must
// hold an address of that family; this is what keeps a misconfigured family
// from falling back to the host.
func (e *exit) dialAddr(ctx context.Context, network string, ip netip.Addr, port string) (net.Conn, error) {
	ip = ip.Unmap()
	family := "tcp6"
	if ip.Is4() {
		family = "tcp4"
	}
	if network != "tcp" && network != family {
		return nil, fmt.Errorf("WARP cannot dial %s as %s", ip, network)
	}
	if !e.supports(family) {
		return nil, fmt.Errorf("WARP %s stack cannot dial %s", e.stack, ip)
	}
	// Dial by the address family of the resolved literal. A bare "tcp"
	// would leave the family implicit, and gVisor resolves that from the
	// stack rather than from the address.
	return e.network.DialContext(ctx, family, net.JoinHostPort(ip.String(), port))
}

func (e *exit) supports(family string) bool {
	switch e.stack {
	case StackIPv4:
		return family == "tcp4"
	case StackIPv6:
		return family == "tcp6"
	case StackDual:
		return true
	default:
		return false
	}
}

// resolve returns destination addresses in preference order. DNS runs on the
// host, not inside the tunnel: the netstack resolver sends UDP to the WARP DNS
// server, and a single dropped query fails every dial on that exit. Host DNS is
// reliable and the connection still leaves from the exit. The tunnel resolver
// remains a fallback for hosts without a working system resolver.
func (e *exit) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addrs) == 0 {
		if fallback, ferr := e.network.LookupContextHost(ctx, host); ferr == nil && len(fallback) > 0 {
			addrs = addrs[:0]
			for _, raw := range fallback {
				if ip, perr := netip.ParseAddr(raw); perr == nil {
					addrs = append(addrs, ip.Unmap())
				}
			}
			err = nil
		}
	}
	if len(addrs) == 0 {
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", host, err)
		}
		return nil, fmt.Errorf("resolve %s: no addresses", host)
	}
	ordered := orderByStack(addrs, e.stack)
	if len(ordered) == 0 {
		return nil, fmt.Errorf("resolve %s: no %s address", host, e.stack)
	}
	return ordered, nil
}

// orderByStack filters addresses to the families the stack can reach and
// prefers IPv6, so IPv6-capable upstreams stay on the per-tunnel /64 quota
// while IPv4-only hosts still connect over IPv4.
func orderByStack(addrs []netip.Addr, stack Stack) []netip.Addr {
	seen := make(map[netip.Addr]bool, len(addrs))
	var v6, v4 []netip.Addr
	for _, ip := range addrs {
		ip = ip.Unmap()
		if seen[ip] {
			continue
		}
		seen[ip] = true
		switch {
		case ip.Is6():
			if stack == StackIPv6 || stack == StackDual {
				v6 = append(v6, ip)
			}
		case ip.Is4():
			if stack == StackIPv4 || stack == StackDual {
				v4 = append(v4, ip)
			}
		}
	}
	return append(v6, v4...)
}

// egressKey is the free-tier quota scope: a /64 for IPv6, a /32 for IPv4. A
// new /128 inside a spent /64 is a cosmetic change, not a fresh identity.
func (e *exit) egressKey() string {
	if e.egress.Is4() {
		return netip.PrefixFrom(e.egress, 32).Masked().String()
	}
	if !e.egress.Is6() {
		return ""
	}
	return netip.PrefixFrom(e.egress, 64).Masked().String()
}

func (e *exit) idle() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.refs == 0 && !e.retired && !e.closed
}
