#!/bin/sh
# Build the legacy netfilter multicall tool with the JetKVM ARM/uClibc SDK.
# Upstream source is GPL-2.0: https://www.netfilter.org/projects/iptables/
set -eu
build_root=${FIREWALL_BUILD_DIR:-/tmp/jetkvm-firewall-build}
sdk=${JETKVM_NATIVE_BUILDKIT:-/opt/jetkvm-native-buildkit}
mkdir -p "$build_root"
cd "$build_root"
curl -fL --max-time 60 -o iptables-1.8.13.tar.xz https://www.netfilter.org/projects/iptables/files/iptables-1.8.13.tar.xz
echo '1afcd33da9e8f913ace6a2126788162e207e26f5d5e29c6573c0e581ffc58b99  iptables-1.8.13.tar.xz' | sha256sum -c -
tar xf iptables-1.8.13.tar.xz
cd iptables-1.8.13
./configure --host=arm-rockchip830-linux-uclibcgnueabihf \
  CC="$sdk/bin/arm-rockchip830-linux-uclibcgnueabihf-gcc" \
  --disable-nftables --disable-shared --enable-static --disable-devel \
  --disable-libipq --disable-connlabel --without-kernel --enable-ipv6 LDFLAGS=-static
make -j4
printf 'Built %s/iptables-1.8.13/iptables/xtables-legacy-multi\n' "$build_root"
