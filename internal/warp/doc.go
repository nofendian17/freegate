// Package warp carries outbound TCP traffic through Cloudflare WARP using a
// userspace WireGuard device and a gVisor network stack. It needs no TUN
// device, root privileges, or system routes: UDP sockets carry WireGuard
// packets, and the netstack terminates TCP inside the process.
//
// Each exit is an independently registered WARP device. A replacement exit
// gets a new public egress; when SourceIPv6 is configured, every exit also
// binds its WireGuard socket to a different address from that routed prefix,
// which is what makes the egress change effective against a /64-scoped
// free-tier quota.
package warp
