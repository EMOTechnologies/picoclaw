#!/bin/sh
set -e

# Source environment variables from secret files if they exist
if [ -f "/root/.picoclaw/env.sh" ]; then
    . /root/.picoclaw/env.sh
fi

# Pass through to VPN entrypoint
exec /vm-vpn-entrypoint.sh "$@"
