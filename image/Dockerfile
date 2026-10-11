# syntax=docker/dockerfile:1.7
# One image, two roles (see docker-compose.yml in the app folder):
#   daemon  - official nordvpnd in the host network namespace (VPN + Meshnet)
#   panel   - web panel; drives the daemon with the official CLI over the shared socket

FROM --platform=$BUILDPLATFORM golang:1.24-bookworm AS panel
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY panel/ ./
RUN go test ./... && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/nordpanel .

FROM ubuntu:24.04
# Leave empty for the latest client at build time, or pin e.g. 4.1.0
ARG NORDVPN_VERSION=""
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && \
    apt-get install -y --no-install-recommends wget ca-certificates apt-transport-https \
        iproute2 iptables procps kmod && \
    wget -qO /etc/apt/trusted.gpg.d/nordvpn_public.asc https://repo.nordvpn.com/gpg/nordvpn_public.asc && \
    echo "deb https://repo.nordvpn.com/deb/nordvpn/debian stable main" > /etc/apt/sources.list.d/nordvpn.list && \
    apt-get update && \
    apt-get install -y --no-install-recommends "nordvpn${NORDVPN_VERSION:+=$NORDVPN_VERSION}" && \
    apt-get purge -y wget && apt-get autoremove -y && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

COPY --from=panel /out/nordpanel /usr/local/bin/nordpanel
COPY --chmod=0755 rootfs/usr/local/bin/ /usr/local/bin/

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["daemon"]
