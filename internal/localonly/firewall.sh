#!/bin/sh
# Device-wide IPv4/IPv6 egress guard. Only our own chain is replaced.
set -eu
for tool in iptables ip6tables iptables-restore ip6tables-restore; do
  command -v "$tool" >/dev/null || { echo "Missing firewall utility: $tool" >&2; exit 1; }
done
# Preflight both families before changing either. No broad flush or policy reset.
iptables -w 2 -S OUTPUT >/dev/null
ip6tables -w 2 -S OUTPUT >/dev/null
if [ "${1:-check}" = check ]; then exit 0; fi
test "$1" = apply
iptables-restore -w 2 --noflush <<'RULES4'
*filter
:JETKVM_LAN_ONLY - [0:0]
-F JETKVM_LAN_ONLY
-A JETKVM_LAN_ONLY -o lo -j RETURN
-A JETKVM_LAN_ONLY -d 10.0.0.0/8 -j RETURN
-A JETKVM_LAN_ONLY -d 172.16.0.0/12 -j RETURN
-A JETKVM_LAN_ONLY -d 192.168.0.0/16 -j RETURN
-A JETKVM_LAN_ONLY -d 127.0.0.0/8 -j RETURN
-A JETKVM_LAN_ONLY -d 169.254.0.0/16 -j RETURN
-A JETKVM_LAN_ONLY -d 224.0.0.0/24 -j RETURN
-A JETKVM_LAN_ONLY -d 255.255.255.255/32 -j RETURN
-A JETKVM_LAN_ONLY -j DROP
COMMIT
RULES4
ip6tables-restore -w 2 --noflush <<'RULES6'
*filter
:JETKVM_LAN_ONLY - [0:0]
-F JETKVM_LAN_ONLY
-A JETKVM_LAN_ONLY -o lo -j RETURN
-A JETKVM_LAN_ONLY -d ::1/128 -j RETURN
-A JETKVM_LAN_ONLY -d fc00::/7 -j RETURN
-A JETKVM_LAN_ONLY -d fe80::/10 -j RETURN
-A JETKVM_LAN_ONLY -d ff02::/16 -j RETURN
-A JETKVM_LAN_ONLY -j DROP
COMMIT
RULES6
# An early jump also covers sockets other daemons opened before the app started.
for tool in iptables ip6tables; do
  first=$("$tool" -w 2 -S OUTPUT | head -n 2 | tail -n 1)
  if [ "$first" != '-A OUTPUT -j JETKVM_LAN_ONLY' ]; then
    "$tool" -w 2 -I OUTPUT 1 -j JETKVM_LAN_ONLY
  fi
done
