# Private-LAN runtime policy

This fork disables vendor cloud access and public-network defaults. The policy
covers the KVM application and its served browser UI. The Windows guest companion
uses paired USB HID and local named pipes; it has no network client or listener.

| Upstream connection | When used | Local-only behavior |
| --- | --- | --- |
| JetKVM cloud API/signaling and Google OIDC | Cloud enrollment/remote sessions | Enrollment and signaling disabled; saved cloud identity/token ignored |
| JetKVM releases API | Automatic and manual update checks, including device ID/SKU | Automatic checks disabled; only explicitly configured local update servers allowed |
| Cloud/public-IP discovery | Cloud-adopted devices | No discovery loop or public-IP probe |
| Google/Cloudflare NTP and HTTP time probes | Default time fallbacks | Only DHCP-provided/private-IP NTP and explicitly configured local HTTP time sources |
| Public PGP key servers and external media URLs | Signature retrieval/media download | Public HTTP endpoints and redirects rejected |
| Tailscale control/relay services | Optional system integration | UI/RPC integration disabled; system-wide filter covers independently running daemons once installed |
| OCR worker/WASM/language models from CDNs | On-demand browser OCR | OCR initialization/downloads disabled; browser external asset loads blocked |
| Prometheus metrics and diagnostics | Local administrator requests | Remain local; no push exporter/crash uploader was found |

## Enforcement

`internal/localonly` validates both the requested hostname and the final numeric
IP before HTTP connections. Private IP literals, localhost, single-label names,
and `.local` names are supported. Public DNS servers, environment/configured
HTTP proxies, public redirects, and DNS answers outside the private network are
rejected. Use private IPs for custom servers with other DNS suffixes.
MQTT and NTP require numeric private IPs; MQTT settings reject hostnames explicitly.

HTTP UI and MCP requests also reject non-private socket peer addresses, ignoring
forwarded-client headers. SSH is an OS service and relies on the system firewall.
Signed local OTA updates still require the trusted signing key to be available
locally; public keyserver retrieval is blocked. A private update URL alone does
not provide that key. Signature verification remains required.

Local WebRTC uses no STUN/TURN servers. Public and relay ICE candidates are
removed, and the Pion socket transport validates final packet destinations after
mDNS resolution. Local multicast discovery remains available.

The browser receives a same-origin Content Security Policy for scripts, fonts,
images, workers and API connections. Existing inline startup/theme code remains
allowed. Help/source links are inert until explicitly opened by the user; they
are not background requests.
DNS prefetching is disabled. After changing the device's static IP, browse to its
new address manually: the same-origin policy blocks the cross-address health probe.

On application startup, an embedded firewall script attempts to install owned
IPv4 and IPv6 OUTPUT chains allowing private/loopback/link-local destinations,
local multicast, and DHCP broadcast, then dropping other destinations. It does
not flush unrelated chains or reset existing policies. Both families are
preflighted. Real `iptables`, `ip6tables` and restore utilities/kernel support
must be verified on the device. If unavailable, application restrictions stay
active and the UI/MCP explicitly report system-wide filtering as **unverified**.
Traffic before the application starts is not covered by this startup hook;
whole-boot isolation requires a base-OS early boot rule or an upstream network ACL.

For the stock image without iptables utilities, `scripts/build_firewall_tools.sh`
builds iptables 1.8.13 against the device SDK (the multicall binary still uses the
device's uClibc). Provision it under `/userdata/jetkvm/firewall/bin`, with links
named `iptables`, `ip6tables`, `iptables-restore`, and `ip6tables-restore`.
Place the reviewed `internal/localonly/firewall.sh` at
`/userdata/jetkvm/firewall/firewall.sh`. `scripts/S05private-lan.sh`, installed
under `/userdata/init.d`, loads the filter modules and applies the policy before
the stock `RkLunch.sh` starts networking. Verify this ordering on other OS images.
These OS support files are separate from the application firmware update.

The policy does not restrict developer build dependencies or explicitly opened
external documentation on the developer's own computer.

## Validation

Tests cover external URL rejection, public redirects, DNS rebinding, public and
relay ICE candidates, final UDP packet destinations after mDNS, continued local
UDP transfers, public NTP rejection, and firewall command/preflight behavior.
Firewall tests use fake tools and do not change the development machine's rules.
Physical firewall availability, reboots and packet capture are hardware checks;
passing source tests does not prove device-wide isolation is installed.
