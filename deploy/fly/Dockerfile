# Fly-only agent image: the published SolidPing image, run as root.
#
# A fly machine has no ping group (net.ipv4.ping_group_range = "1 0") and the
# stock image runs as nonroot (65532) without CAP_NET_RAW, so ICMP checks fail
# with "socket: operation not permitted". Root inside the single-tenant
# Firecracker VM has CAP_NET_RAW. See README.md, "ICMP checks".
ARG SOLIDPING_TAG=latest
FROM ghcr.io/fclairamb/solidping:${SOLIDPING_TAG}

USER 0:0
