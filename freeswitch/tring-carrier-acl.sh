#!/bin/sh
# Emits <node> entries for acl.conf.xml's "carriers" list (included via
# X-PRE-PROCESS cmd="exec"), from TRING_CARRIER_CIDRS — the comma-separated
# addresses your carriers send SIP from. The external (trunk) profile only
# accepts inbound calls from these. Empty = allow any source (the default, so
# existing deployments keep working until the list is set).
#
# Each entry must look like an IPv4/IPv6 address or CIDR; anything else is
# dropped (so the env var can't inject XML) and reported on stderr.
# Loopback and TRING_INTERNAL_IP are always allowed: the media gateway's own
# SIP traffic to this profile.
set -f
if [ -z "$TRING_CARRIER_CIDRS" ]; then
    echo '<node type="allow" cidr="0.0.0.0/0"/>'
    echo '<node type="allow" cidr="::/0"/>'
    exit 0
fi
echo '<node type="allow" cidr="127.0.0.0/8"/>'
echo '<node type="allow" cidr="::1/128"/>'
for c in ${TRING_INTERNAL_IP:+$TRING_INTERNAL_IP/32} $(echo "$TRING_CARRIER_CIDRS" | tr ',' ' '); do
    if echo "$c" | grep -Eq '^([0-9]{1,3}\.){3}[0-9]{1,3}(/[0-9]{1,2})?$|^[0-9A-Fa-f:]+(/[0-9]{1,3})?$'; then
        case "$c" in */*) ;; *:*) c="$c/128" ;; *) c="$c/32" ;; esac
        echo "<node type=\"allow\" cidr=\"$c\"/>"
    else
        echo "tring-carrier-acl: ignoring invalid entry '$c'" >&2
    fi
done
