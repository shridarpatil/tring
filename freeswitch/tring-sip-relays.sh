#!/bin/sh
# Generates SIP relay rules from TRING_SIP_RELAYS, so this FreeSWITCH can share
# its carrier trunk with other Tring servers (a UAT box, a dedicated instance):
# calls to a relay's numbers are handed to that server, and calls FROM that
# server go out over the carrier.
#
#   TRING_SIP_RELAYS="uat=10.0.5.20:5080:+912265915001-+912265915030;acme=10.0.6.30:5080:+912265915089"
#   TRING_SIP_RELAY_GATEWAY=t_4cc0a6963ea0   (the carrier trunk's gateway name)
#
# Each relay is name=ipv4:port:numbers, relays separated by ';'. numbers is a
# comma-separated list of E.164 numbers and/or A-B ranges (same length, at most
# 1000 numbers per relay). '+' is optional when matching.
#
#   tring-sip-relays dialplan  -> <extension>s for the 'public' context
#                                 (included via X-PRE-PROCESS cmd="exec")
#   tring-sip-relays acl       -> <node>s adding each relay IP to 'carriers'
#
# Outbound calls from a relay keep their caller ID only when it is one of the
# relay's own numbers; anything else is replaced by the relay's first number,
# so a relay can never present another relay's (or this server's) numbers.
# A malformed relay is dropped whole and reported on stderr, so the env var
# can't inject XML and a typo can't break the rest of the dialplan.
set -f
mode=${1:-dialplan}
max=1000

err() { echo "tring-sip-relays: $*" >&2; }

# expand SPECS: prints one number per line (as written, '+' kept), or fails.
expand() {
    for s in $(echo "$1" | tr ',' ' '); do
        case "$s" in
            *-*) a=${s%-*}; b=${s#*-} ;;
            *) a=$s; b=$s ;;
        esac
        for n in "$a" "$b"; do
            echo "$n" | grep -Eq '^\+?[1-9][0-9]{6,14}$' || { err "bad number '$n'"; return 1; }
        done
        p=""
        case "$a" in +*) p="+" ;; esac
        ad=${a#+}; bd=${b#+}
        if [ ${#ad} -ne ${#bd} ] || [ "$ad" -gt "$bd" ]; then
            err "bad range '$s'"; return 1
        fi
        if [ $((bd - ad)) -ge $max ]; then
            err "range '$s' is over $max numbers"; return 1
        fi
        n=$ad
        while [ "$n" -le "$bd" ]; do
            echo "$p$n"
            n=$((n + 1))
        done
    done
}

gw=$TRING_SIP_RELAY_GATEWAY
if [ -n "$gw" ] && ! echo "$gw" | grep -Eq '^[A-Za-z0-9_.-]+$'; then
    err "ignoring invalid TRING_SIP_RELAY_GATEWAY '$gw'"; gw=""
fi

out=""
in=""
IFS_SAVE=$IFS
IFS=';'
for r in $TRING_SIP_RELAYS; do
    IFS=$IFS_SAVE
    [ -z "$r" ] && continue
    name=${r%%=*}; rest=${r#*=}
    ip=${rest%%:*}; rest=${rest#*:}
    port=${rest%%:*}; specs=${rest#*:}
    if ! echo "$name" | grep -Eq '^[A-Za-z0-9_-]{1,32}$' ||
        ! echo "$ip" | grep -Eq '^([0-9]{1,3}\.){3}[0-9]{1,3}$' ||
        ! echo "$port" | grep -Eq '^[0-9]{1,5}$' || [ "$port" -gt 65535 ] || [ -z "$specs" ]; then
        err "ignoring invalid relay '$r' (want name=ipv4:port:numbers)"; IFS=';'; continue
    fi
    if ! nums=$(expand "$specs"); then
        err "ignoring relay '$name'"; IFS=';'; continue
    fi
    if [ "$(echo "$nums" | wc -l)" -gt $max ]; then
        err "ignoring relay '$name': over $max numbers"; IFS=';'; continue
    fi

    if [ "$mode" = acl ]; then
        echo "<node type=\"allow\" cidr=\"$ip/32\"/>"
        IFS=';'; continue
    fi

    first=$(echo "$nums" | head -n 1)
    alt=$(echo "$nums" | sed 's/^+//' | paste -sd '|' -)
    ipre=$(echo "$ip" | sed 's/\./\\./g')
    if [ -n "$gw" ]; then
        bridge="<action application=\"bridge\" data=\"sofia/gateway/$gw/\$1\"/>"
    else
        err "relay '$name': TRING_SIP_RELAY_GATEWAY unset, its outbound calls are rejected"
        bridge="<action application=\"hangup\" data=\"CALL_REJECTED\"/>"
    fi
    # From the relay: out over the carrier. break="never" lets a foreign caller
    # ID fall through to its anti-action (pin to the relay's first number).
    out="$out
    <extension name=\"sip_relay_out_$name\">
      <condition field=\"network_addr\" expression=\"^$ipre\$\"/>
      <condition field=\"caller_id_number\" expression=\"^\\+?($alt)\$\" break=\"never\">
        <anti-action application=\"set\" data=\"effective_caller_id_number=$first\"/>
      </condition>
      <condition field=\"destination_number\" expression=\"^(\\+?\\d+)\$\">
        $bridge
        <anti-action application=\"hangup\" data=\"UNALLOCATED_NUMBER\"/>
      </condition>
    </extension>"
    # To the relay's numbers: hand the call to that server.
    in="$in
    <extension name=\"sip_relay_in_$name\">
      <condition field=\"destination_number\" expression=\"^(\\+?($alt))\$\">
        <action application=\"bridge\" data=\"sofia/external/\$1@$ip:$port\"/>
      </condition>
    </extension>"
    IFS=';'
done
IFS=$IFS_SAVE

if [ "$mode" = dialplan ]; then
    # Outbound rules first: a relay's own traffic must never match an inbound
    # rule (or the gateway catch-all after these).
    echo "<!-- tring-sip-relays: generated from TRING_SIP_RELAYS -->$out$in"
fi
