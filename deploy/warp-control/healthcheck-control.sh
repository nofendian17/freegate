#!/bin/bash
# Healthy means both halves work: the WARP tunnel is connected (the image's
# own check) and the rotation API is answering. A container that can carry
# traffic but cannot rotate would only surface as failed free-tier retries.
set -e

/healthcheck/index.sh

curl -fsS --max-time 3 "http://127.0.0.1:${CONTROL_PORT:-8081}/healthz" >/dev/null
