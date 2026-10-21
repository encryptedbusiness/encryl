#!/usr/bin/env bash
# Run inside the Linux guest (deploy/lima.yaml already runs this). Kept as a
# separate script so an existing VM or a bare-metal box can be prepared with:
#
#   sudo deploy/lima-bootstrap.sh
#
# It is idempotent.
set -euxo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "run as root (sudo)" >&2
  exit 1
fi

# 1. kTLS module, now and at boot.
modprobe tls
echo tls > /etc/modules-load.d/encryl-tls.conf
test -e /proc/net/tls_stat

# 2. Connection-density sysctls. Keep in sync with docs/operations.md.
cat > /etc/sysctl.d/99-encryl.conf <<'EOF'
net.core.somaxconn = 65535
net.ipv4.tcp_max_syn_backlog = 65535
net.core.netdev_max_backlog = 65535
net.ipv4.ip_local_port_range = 10240 65535
net.ipv4.tcp_tw_reuse = 1
net.ipv4.tcp_fin_timeout = 15
fs.file-max = 2097152
fs.nr_open = 2097152
EOF
sysctl --system

# 3. Descriptor limits for the invoking user.
TARGET_USER="${SUDO_USER:-$(id -un)}"
cat > /etc/security/limits.d/99-encryl.conf <<EOF
${TARGET_USER} soft nofile 1048576
${TARGET_USER} hard nofile 1048576
EOF

# 4. Verify, do not assume.
uname -r
ls -l /proc/net/tls_stat
sysctl net.core.somaxconn
grep -c . /proc/sys/fs/file-max
