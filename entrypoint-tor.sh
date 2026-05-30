#!/bin/bash
set -e

if [ -z "$TOR_PASS" ]; then
    TOR_PASS=$(tr -dc A-Za-z0-9 </dev/urandom | head -c 32)
    echo "Generated random Tor control password" >&2
fi

HASHED=$(tor --hash-password "$TOR_PASS" 2>/dev/null | tail -1)
sed -i "s|^HashedControlPassword.*|HashedControlPassword $HASHED|" /etc/tor/torrc

echo "$TOR_PASS" > /tmp/tor-control-pass
chmod 600 /tmp/tor-control-pass

export TOR_PASS

exec tor -f /etc/tor/torrc
