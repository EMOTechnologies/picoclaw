#!/bin/sh
set -e

# OpenVPN startup script for Cloud Run
# This establishes VPN connection before starting the application

VPN_CONFIG_SOURCE="/etc/openvpn-config/client.ovpn"
VPN_AUTH_SOURCE="/etc/openvpn-auth/auth.txt"

# Writable directory for OpenVPN
VPN_WORK_DIR="/tmp/openvpn"
VPN_CONFIG="${VPN_WORK_DIR}/client.ovpn"
VPN_AUTH="${VPN_WORK_DIR}/auth.txt"

# Check if VPN credentials are provided
if [ ! -f "${VPN_CONFIG_SOURCE}" ]; then
    echo "ERROR: OpenVPN config not found at ${VPN_CONFIG_SOURCE}"
    echo "Mount client.ovpn to ${VPN_CONFIG_SOURCE}"
    exit 1
fi

if [ ! -f "${VPN_AUTH_SOURCE}" ]; then
    echo "ERROR: OpenVPN auth not found at ${VPN_AUTH_SOURCE}"
    echo "Mount auth.txt to ${VPN_AUTH_SOURCE}"
    exit 1
fi

# Create working directory and copy files (secrets are read-only)
mkdir -p "${VPN_WORK_DIR}"
cp "${VPN_CONFIG_SOURCE}" "${VPN_CONFIG}"
cp "${VPN_AUTH_SOURCE}" "${VPN_AUTH}"
chmod 600 "${VPN_CONFIG}" "${VPN_AUTH}"

# Update config to use auth file
if ! grep -q "auth-user-pass ${VPN_AUTH}" "${VPN_CONFIG}"; then
    sed -i "s|auth-user-pass|auth-user-pass ${VPN_AUTH}|g" "${VPN_CONFIG}"
fi

echo "Starting OpenVPN connection..."
# Start openvpn in background with log output
openvpn --config "${VPN_CONFIG}" \
    --daemon \
    --log /var/log/openvpn.log \
    --status /var/log/openvpn-status.log 10

# Wait for VPN connection to establish
echo "Waiting for VPN tunnel to establish..."
for i in $(seq 1 30); do
    if ip addr show tun0 2>/dev/null | grep -q "inet "; then
        VPN_IP=$(ip addr show tun0 | grep "inet " | awk '{print $2}')
        echo "VPN connected successfully! Tunnel IP: ${VPN_IP}"
        break
    fi
    if [ $i -eq 30 ]; then
        echo "ERROR: VPN connection timeout after 30 seconds"
        echo "=== OpenVPN Log ==="
        cat /var/log/openvpn.log
        exit 1
    fi
    sleep 1
done

# Verify connectivity through VPN (optional DNS test)
echo "VPN connection established. Starting application..."

# Now run picoclaw-launcher with passed arguments
exec picoclaw-launcher "$@"
