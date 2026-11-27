#!/bin/sh
# Prints one carrier-leg codec setting for vars.xml (X-PRE-PROCESS exec-set),
# from TRING_CARRIER_CODEC:
#   g711 (default)  G.711 only, the carrier's own order; inbound relays to the
#                   plug untranscoded.
#   g729            G.729 first (~24 kbps each way vs ~80), G.711 fallback.
#                   Needs the image's `g729` build target (mod_bcg729).
#   amr             AMR 12.2 (~29 kbps each way), G.711 fallback. mod_amr and
#                   opencore-amrnb ship in every image.
# A compressed codec is transcoded to the plug's G.711 by FS, and negotiation
# turns greedy (our order wins) because carriers list G.711 first.
# An unknown value falls back to g711.
#
# Usage: tring-carrier-codecs prefs|pcma|pcmu|negotiation
codec=$TRING_CARRIER_CODEC
case $codec in
g729) lead=G729, ;;
amr) lead=AMR, ;;
'' | g711) lead= ;;
*)
	echo "tring-carrier-codecs: unknown TRING_CARRIER_CODEC '$codec', using g711" >&2
	lead=
	;;
esac
case $1 in
prefs | pcma) echo "${lead}PCMA,PCMU" ;;
pcmu) echo "${lead}PCMU,PCMA" ;;
negotiation) if [ -n "$lead" ]; then echo greedy; else echo generous; fi ;;
*)
	echo "usage: tring-carrier-codecs prefs|pcma|pcmu|negotiation" >&2
	exit 2
	;;
esac
