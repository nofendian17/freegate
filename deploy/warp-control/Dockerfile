# WARP sidecar with a small rotation API on top of the official Cloudflare
# client. Based on caomingjun/warp, which already provides:
#   - warp-cli + warp-svc (official client, MASQUE tunnel)
#   - gost SOCKS5 listener on 0.0.0.0:1080
#   - registration persisted in /var/lib/cloudflare-warp
#
# We only add the control API used by freegate to rotate the exit identity.
FROM caomingjun/warp:latest

USER root
COPY warp-control.py /usr/local/bin/warp-control.py
COPY entrypoint-control.sh /usr/local/bin/entrypoint-control.sh
COPY healthcheck-control.sh /usr/local/bin/healthcheck-control.sh
RUN chmod 0755 /usr/local/bin/warp-control.py /usr/local/bin/entrypoint-control.sh /usr/local/bin/healthcheck-control.sh
USER warp

EXPOSE 1080 8081

HEALTHCHECK --interval=15s --timeout=5s --start-period=30s --retries=3 \
  CMD /usr/local/bin/healthcheck-control.sh

ENTRYPOINT ["/usr/local/bin/entrypoint-control.sh"]
