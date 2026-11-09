#!/bin/sh
# Install as /userdata/init.d/S05private-lan.sh after provisioning the tools
# and reviewed firewall.sh under /userdata/jetkvm/firewall.
set -eu
root=/userdata/jetkvm/firewall
for tool in iptables ip6tables iptables-restore ip6tables-restore; do
  if [ ! -e "/usr/sbin/$tool" ]; then
    ln -s "$root/bin/$tool" "/usr/sbin/$tool"
  fi
done
modprobe iptable_filter
modprobe ip6table_filter
PATH="$root/bin:$PATH" sh "$root/firewall.sh" apply
