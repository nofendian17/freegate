#!/bin/bash
# Start the rotation control API next to the image's normal entrypoint.
# The control API is what the gateway calls; everything else (warp-svc,
# registration, gost) stays exactly as the base image ships it.
set -e

python3 /usr/local/bin/warp-control.py &

exec /entrypoint.sh
