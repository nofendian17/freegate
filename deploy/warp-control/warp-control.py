#!/usr/bin/env python3
"""Rotation control API for one WARP sidecar.

Runs inside the WARP container next to the official Cloudflare client. The
gateway (freegate) calls POST /rotate when OpenCode reports a free-tier usage
limit; the sidecar then asks WARP for a new exit identity and reports the new
address.

Why in-container: the rotation needs warp-cli, which talks to the local
warp-svc over a unix socket. Exposing the Docker socket to the gateway would
hand it root-equivalent control of the host, so the API stays narrow: rotate
one container, report one IP.

Endpoints:
  GET  /ip      -> {"ip": "...", "status": "..."}
  POST /rotate  -> {"ip": "...", "previous": "..."}
  GET  /healthz -> "ok"

Environment:
  CONTROL_PORT       listen port (default 8081)
  WARP_SOCKS         SOCKS5 listener used for the egress check (127.0.0.1:1080)
  WARP_TRACE_URL     URL queried through SOCKS5 (Cloudflare trace)
  WARP_ROTATE_TIMEOUT  seconds to wait for a new IP (default 45)
"""

import json
import os
import re
import subprocess
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = int(os.environ.get("CONTROL_PORT", "8081"))
SOCKS = os.environ.get("WARP_SOCKS", "127.0.0.1:1080")
TRACE_URL = os.environ.get(
    "WARP_TRACE_URL", "https://[2606:4700:4700::1111]/cdn-cgi/trace"
)
ROTATE_TIMEOUT = float(os.environ.get("WARP_ROTATE_TIMEOUT", "45"))

# warp-cli is used by the image's own entrypoint as the unprivileged warp
# user, so no sudo is needed here either.
WARP_CLI = ["warp-cli", "--accept-tos"]

_rotate_lock = threading.Lock()

_IP_RE = re.compile(r"^ip=(.+)$", re.MULTILINE)


def run_cli(*args, timeout=20):
    """Run one warp-cli command; return (ok, output)."""
    try:
        proc = subprocess.run(
            WARP_CLI + list(args),
            capture_output=True,
            text=True,
            timeout=timeout,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        return False, str(exc)
    out = (proc.stdout or "") + (proc.stderr or "")
    return proc.returncode == 0, out.strip()


def status():
    ok, out = run_cli("status", timeout=10)
    if not ok:
        return "unavailable"
    for line in out.splitlines():
        if line.lower().startswith("status update:"):
            return line.split(":", 1)[1].strip()
    return out.splitlines()[0] if out else "unknown"


def egress_ip():
    """Return the exit IP as seen through the SOCKS listener.

    The check goes through gost, not the container's own socket: the host IP
    and the WARP IP are different addresses, and only the proxied path is what
    the gateway actually uses.
    """
    try:
        proc = subprocess.run(
            [
                "curl",
                "-sS",
                "--max-time",
                "8",
                "--socks5-hostname",
                SOCKS,
                TRACE_URL,
            ],
            capture_output=True,
            text=True,
            timeout=12,
        )
    except (OSError, subprocess.TimeoutExpired):
        return ""
    if proc.returncode != 0:
        return ""
    match = _IP_RE.search(proc.stdout or "")
    return match.group(1).strip() if match else ""


def rotate():
    """Ask WARP for a new identity and wait until the exit IP changes.

    rotate-keys keeps the device registration: it replaces the tunnel keys
    instead of calling Cloudflare's registration API again, which is rate
    limited and would eventually lock the device out.
    """
    previous = egress_ip()

    for args in (
        ("tunnel", "rotate-keys"),
        ("tunnel", "endpoint", "reset"),
        ("disconnect",),
    ):
        ok, out = run_cli(*args)
        if not ok:
            # A failed step is not fatal by itself: the reconnect below may
            # still produce a new identity, and the IP check is the real
            # success criterion.
            print(f"warp-control: {args[0]} failed: {out}", flush=True)

    time.sleep(1.5)
    run_cli("connect")

    deadline = time.monotonic() + ROTATE_TIMEOUT
    while time.monotonic() < deadline:
        current = egress_ip()
        if current and current != previous:
            return current, previous, ""
        time.sleep(2)

    return "", previous, f"exit IP unchanged after {ROTATE_TIMEOUT:.0f}s"


class Handler(BaseHTTPRequestHandler):
    server_version = "warp-control/1"

    def log_message(self, fmt, *args):
        print(f"warp-control: {self.address_string()} {fmt % args}", flush=True)

    def _send(self, code, payload):
        body = payload if isinstance(payload, bytes) else json.dumps(payload).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == "/healthz":
            self._send(200, {"ok": True})
            return
        if self.path == "/ip":
            self._send(200, {"ip": egress_ip(), "status": status()})
            return
        self._send(404, {"error": "not found"})

    def do_POST(self):
        if self.path != "/rotate":
            self._send(404, {"error": "not found"})
            return
        # A burst of 429s from one exit arrives together. A caller that
        # finds a rotation already running gets the resulting identity
        # instead of starting a second rotation, which would burn another
        # address for the same limit.
        contended = _rotate_lock.locked()
        if not _rotate_lock.acquire(timeout=ROTATE_TIMEOUT):
            self._send(503, {"error": "rotation already in progress"})
            return
        try:
            if contended:
                current = egress_ip()
                self._send(
                    200,
                    {"ip": current, "previous": current, "coalesced": True},
                )
                return
            current, previous, err = rotate()
        finally:
            _rotate_lock.release()
        if err:
            self._send(502, {"error": err, "previous": previous})
            return
        print(
            f"warp-control: rotated {previous or '?'} -> {current}",
            flush=True,
        )
        self._send(200, {"ip": current, "previous": previous})


def main():
    server = ThreadingHTTPServer(("0.0.0.0", PORT), Handler)
    print(f"warp-control: listening on :{PORT}", flush=True)
    server.serve_forever()


if __name__ == "__main__":
    main()
