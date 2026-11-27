# syntax=docker/dockerfile:1.7
#
# Tring FreeSWITCH image: safarov/freeswitch with Tring's STATIC config baked
# into the vanilla template dir. The safarov entrypoint copies vanilla→/etc/
# freeswitch on every boot, so baking our files into vanilla makes them the
# live config — no bind mounts, no rsync-to-host step.
#
# The DYNAMIC trunk dir (sip_profiles/external/dynamic) is deliberately NOT
# baked: the API writes per-trunk gateway XML there at runtime and rescans the
# external profile over ESL, so it MUST stay a mounted volume. We only create
# the empty dir so external.xml's `include external/dynamic/*.xml` resolves
# before the first trunk exists.
#
# Build (context = the freeswitch/ dir):
#   docker build -t ghcr.io/shridarpatil/tring-freeswitch:develop \
#     -f freeswitch/Dockerfile freeswitch
# G.729 transcoder (mod_bcg729), for the opt-in `g729` target only. The base
# image's mod_g729 is passthrough only, so a carrier leg in G.729
# (TRING_CARRIER_G729=true) needs a real encoder/decoder to reach the plug's
# G.711. bcg729 is GPLv3 and FreeSWITCH is MPL 1.1, so the default image —
# the one CI publishes — never contains it; operators who want G.729 build
# `--target g729` for their own use. Built against the FreeSWITCH
# headers of the exact release in the base image (v1.11.3, Debian 13 / glibc
# 2.41 — keep these refs and the builder distro in step when bumping the base).
# The image ships no headers, so they come from a sparse checkout; the only
# configure-generated one, switch_am_config.h, is just LP64 type names.
FROM debian:trixie-slim AS bcg729
RUN apt-get update \
 && apt-get install -y --no-install-recommends build-essential cmake git ca-certificates \
 && rm -rf /var/lib/apt/lists/*
ARG FS_REF=ef32e205295e29f034f1453ad245ba5efb07b94a
ARG MOD_BCG729_REF=4203247dee4719545005ec7ab9ea536fc83df1d8
WORKDIR /build
RUN git init -q fs && cd fs \
 && git remote add origin https://github.com/signalwire/freeswitch \
 && git sparse-checkout set --no-cone /src/include/ /libs/libteletone/src/ \
 && git fetch -q --depth 1 --filter=blob:none origin "$FS_REF" \
 && git checkout -q FETCH_HEAD \
 && mkdir /build/inc \
 && cp src/include/*.h libs/libteletone/src/*.h /build/inc/ \
 && sed -e 's/@short_value@/short/' -e 's/@int_value@/int/' -e 's/@long_value@/long/' \
        -e 's/@size_t_value@/size_t/' -e 's/@ssize_t_value@/ssize_t/' -e 's/@voidp_size@/8/' \
        -e 's|@prefix@|/usr|' \
        -e 's/@ssize_t_fmt@/#define SWITCH_SSIZE_T_FMT "ld"/' \
        -e 's/@size_t_fmt@/#define SWITCH_SIZE_T_FMT "ld"/' \
        -e 's/@int64_t_fmt@/#define SWITCH_INT64_T_FMT "ld"/' \
        -e 's/@uint64_t_fmt@/#define SWITCH_UINT64_T_FMT "lu"/' \
        src/include/switch_am_config.h.in > /build/inc/switch_am_config.h
# The module's Makefile clones bcg729 and pins it to tag 1.1.1 itself.
RUN git clone -q https://github.com/xadhoom/mod_bcg729 \
 && cd mod_bcg729 && git checkout -q "$MOD_BCG729_REF" \
 && make FS_INCLUDES=/build/inc

# Pinned by digest (was :latest) so a rebuild can't silently pull a different
# FreeSWITCH. Bump deliberately: docker pull safarov/freeswitch:latest, then
# docker inspect --format '{{index .RepoDigests 0}}' safarov/freeswitch:latest
FROM safarov/freeswitch@sha256:f156e01a96d48944078ed7f0e24304be63ea15f6466cf607a0aeb6cb47761da7 AS fs

# Static config → vanilla templates (mirrors the docker-compose :ro mounts).
COPY --chmod=755 tring-carrier-acl.sh             /usr/local/bin/tring-carrier-acl
COPY --chmod=755 tring-sip-relays.sh              /usr/local/bin/tring-sip-relays
COPY conf/vars.xml                               /usr/share/freeswitch/conf/vanilla/vars.xml
COPY conf/dialplan/default.xml                   /usr/share/freeswitch/conf/vanilla/dialplan/default.xml
COPY conf/dialplan/public.xml                     /usr/share/freeswitch/conf/vanilla/dialplan/public.xml
COPY conf/dialplan/whatsapp.xml                  /usr/share/freeswitch/conf/vanilla/dialplan/whatsapp.xml
COPY conf/directory/default.xml                  /usr/share/freeswitch/conf/vanilla/directory/default.xml
COPY conf/autoload_configs/modules.conf.xml      /usr/share/freeswitch/conf/vanilla/autoload_configs/modules.conf.xml
COPY conf/autoload_configs/acl.conf.xml          /usr/share/freeswitch/conf/vanilla/autoload_configs/acl.conf.xml
COPY conf/autoload_configs/event_socket.conf.xml /usr/share/freeswitch/conf/vanilla/autoload_configs/event_socket.conf.xml
COPY conf/autoload_configs/local_stream.conf.xml /usr/share/freeswitch/conf/vanilla/autoload_configs/local_stream.conf.xml
COPY conf/autoload_configs/switch.conf.xml       /usr/share/freeswitch/conf/vanilla/autoload_configs/switch.conf.xml
COPY conf/sip_profiles/external.xml              /usr/share/freeswitch/conf/vanilla/sip_profiles/external.xml
COPY conf/sip_profiles/internal.xml              /usr/share/freeswitch/conf/vanilla/sip_profiles/internal.xml
COPY conf/sip_profiles/external/tring-gw.xml     /usr/share/freeswitch/conf/vanilla/sip_profiles/external/tring-gw.xml
COPY conf/sip_profiles/whatsapp.xml              /usr/share/freeswitch/conf/vanilla/sip_profiles/whatsapp.xml
COPY conf/sip_profiles/whatsapp/profile.xml      /usr/share/freeswitch/conf/vanilla/sip_profiles/whatsapp/profile.xml

# Drop the stock IPv6 profiles: they would otherwise load next to ours on
# IPv6 hosts with none of Tring's settings (ACLs, bindings).
RUN rm -f /usr/share/freeswitch/conf/vanilla/sip_profiles/*-ipv6.xml

# Drop the stock demo dialplans (pizza demo, talking clock, example.com
# gateway bridges) that default.xml's `include default/*.xml` would load, the
# unused public/ samples, and the stock features / skinny-patterns contexts.
# Tring routes only through its own extensions.
RUN rm -rf /usr/share/freeswitch/conf/vanilla/dialplan/default/* \
           /usr/share/freeswitch/conf/vanilla/dialplan/public/* \
           /usr/share/freeswitch/conf/vanilla/dialplan/features.xml \
           /usr/share/freeswitch/conf/vanilla/dialplan/skinny-patterns.xml \
           /usr/share/freeswitch/conf/vanilla/dialplan/skinny-patterns

# Dynamic trunk dir stays a runtime volume — just ensure the (empty) path exists.
RUN mkdir -p /usr/share/freeswitch/conf/vanilla/sip_profiles/external/dynamic \
 && mkdir -p /etc/freeswitch/sip_profiles/external/dynamic

# Recordings spool: seed the volume world-writable (sticky) so the NON-ROOT
# distroless services (gateway: media-plane recordings; orchestrator: S3 upload
# + cleanup) can write alongside root-owned FreeSWITCH recordings. A named
# volume inherits this dir's mode on first creation.
RUN mkdir -p /usr/share/freeswitch/recordings/tring \
    && chmod 1777 /usr/share/freeswitch/recordings/tring

# Opt-in G.729: docker build --target g729 -f freeswitch/Dockerfile freeswitch
FROM fs AS g729
COPY --from=bcg729 /build/mod_bcg729/mod_bcg729.so /usr/lib/freeswitch/mod/mod_bcg729.so
RUN sed -i 's|<!-- tring:g729 -->|<load module="mod_bcg729"/>|' \
      /usr/share/freeswitch/conf/vanilla/autoload_configs/modules.conf.xml

# Default target (last stage): no G.729 transcoder.
FROM fs
