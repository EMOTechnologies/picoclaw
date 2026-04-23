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

# Get the default gateway before VPN starts (to preserve SSH access)
DEFAULT_GW=$(ip route | grep default | awk '{print $3}' | head -1)
DEFAULT_IFACE=$(ip route | grep default | awk '{print $5}' | head -1)

echo "Preserving SSH access: gateway=${DEFAULT_GW} interface=${DEFAULT_IFACE}"

# Add route-nopull to prevent VPN from becoming default gateway
if ! grep -q "route-nopull" "${VPN_CONFIG}"; then
    echo "route-nopull" >> "${VPN_CONFIG}"
fi

# Add GovTech Singapore IP ranges through VPN
echo "Adding GovTech Singapore CIDR blocks through VPN..."
cat >> "${VPN_CONFIG}" << 'ROUTES'
# GovTech Singapore (AS135008) - covers Singpass, MOM, and all gov services
route 160.96.0.0 255.255.0.0 vpn_gateway
route 103.1.136.0 255.255.252.0 vpn_gateway
route 202.166.124.0 255.255.255.0 vpn_gateway

# Singpass CloudFront IPs (login.id.singpass.gov.sg)
route 13.33.88.80 255.255.255.255 vpn_gateway
route 13.33.88.101 255.255.255.255 vpn_gateway
route 13.33.88.84 255.255.255.255 vpn_gateway
route 13.33.88.88 255.255.255.255 vpn_gateway

# Test site - route icanhazip.com through VPN for verification
route 104.16.184.241 255.255.255.255 vpn_gateway
route 104.16.185.241 255.255.255.255 vpn_gateway
ROUTES

# Configure DNS to avoid DNS leakage
echo "Configuring DNS..."
cat >> "${VPN_CONFIG}" << 'DNS'
# Use public DNS to avoid DNS leakage
dhcp-option DNS 8.8.8.8
dhcp-option DNS 1.1.1.1
# Or use your home router DNS if you know the IP:
# dhcp-option DNS 192.168.1.1
DNS

echo "Starting OpenVPN connection with split tunneling..."
# Start openvpn with split tunneling - most traffic stays on GCP
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

# With route-nopull, default route stays through GCP
# GCP metadata and SSH access automatically preserved
echo "Split tunneling active - GovTech Singapore IP ranges via VPN"
echo "Verifying routes..."

# Show VPN gateway
VPN_GW=$(ip route | grep "via.*tun0" | head -1 | awk '{print $3}')
echo "  VPN Gateway: ${VPN_GW}"

# Test route to Singpass
echo "  Testing route to 160.96.0.0/16 (GovTech)..."
ip route get 160.96.0.1 2>/dev/null || echo "  Route pending (will be active after VPN fully connected)"

echo ""
echo "Routing summary:"
echo "  - GovTech Singapore (160.96.0.0/16, 103.1.136.0/22, 202.166.124.0/24): via VPN"
echo "  - icanhazip.com: via VPN (for testing)"
echo "  - Everything else: via GCP (fast path)"

# Clean up stale PID files from previous runs
echo "Cleaning up stale PID files..."
rm -f /root/.picoclaw/*.pid

# Now run picoclaw-launcher with passed arguments
exec picoclaw-launcher "$@"
