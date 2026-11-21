#!/bin/sh
# Emits <node> entries for acl.conf.xml's "carriers" list (included via
# X-PRE-PROCESS cmd="exec") — the addresses your carriers send SIP from, as set
# on trunks in the console (carrier source IPs). The api writes them, one per
# line, to carriers.acl in the shared dynamic trunk dir and runs `reloadacl`,
# which re-runs this script. The external (trunk) profile only accepts inbound
# calls from these. No file, or no addresses on any trunk =
# allow any source (so a fresh install accepts calls until one is set).
#
# Each entry must look like an IPv4/IPv6 address or CIDR; anything else is
# dropped (so the file can't inject XML) and reported on stderr.
# Loopback and TRING_INTERNAL_IP are always allowed: the media gateway's own
# SIP traffic to this profile.
set -f
file=${1:-/etc/freeswitch/sip_profiles/external/dynamic/carriers.acl}
entries=""
[ -r "$file" ] && entries=$(cat "$file")
if [ -z "$(echo "$entries" | tr -d '[:space:]')" ]; then
    echo '<node type="allow" cidr="0.0.0.0/0"/>'
    echo '<node type="allow" cidr="::/0"/>'
    exit 0
fi
echo '<node type="allow" cidr="127.0.0.0/8"/>'
echo '<node type="allow" cidr="::1/128"/>'
for c in ${TRING_INTERNAL_IP:+$TRING_INTERNAL_IP/32} $entries; do
    if echo "$c" | grep -Eq '^([0-9]{1,3}\.){3}[0-9]{1,3}(/[0-9]{1,2})?$|^[0-9A-Fa-f:]+(/[0-9]{1,3})?$'; then
        case "$c" in */*) ;; *:*) c="$c/128" ;; *) c="$c/32" ;; esac
        echo "<node type=\"allow\" cidr=\"$c\"/>"
    else
        echo "tring-carrier-acl: ignoring invalid entry '$c'" >&2
    fi
done
