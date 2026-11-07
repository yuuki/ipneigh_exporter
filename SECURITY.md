# Security policy

ipneigh_exporter runs with `CAP_NET_ADMIN` (or as root) to subscribe to the
kernel's rtnetlink neighbour events, and serves Prometheus metrics over HTTP
without authentication or TLS. Interface names, VRF names, IP and MAC
addresses come from the kernel and end up in labels and logs. Issues that let
someone crash the exporter, make it misreport flaps, exhaust its memory or
connections, or gain anything from its capability are in scope. That the
metrics endpoint is unauthenticated is by design: restrict access to it with
the listen address, a firewall or a reverse proxy.

## Supported versions

Security fixes are made on the `main` branch and released in the next
version. Only the latest release is supported.

## Reporting a vulnerability

Please do **not** open a public issue, pull request or discussion.

Report the vulnerability privately with GitHub's
[private vulnerability reporting](https://github.com/yuuki/ipneigh_exporter/security/advisories/new)
(the "Report a vulnerability" button of the Security tab). If that is not
possible, contact the author privately via the contact information on
https://github.com/yuuki.

Please include the exporter version (`ipneigh_exporter --version`), the
command line or systemd unit, the kernel version, what an attacker controls
(network access to the metrics port, a host on the local network, interface
configuration, ...), and the impact. You should receive an answer within a few
weeks; ipneigh_exporter is maintained on a best-effort basis.
