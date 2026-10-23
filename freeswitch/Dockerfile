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
FROM safarov/freeswitch:latest

# Static config → vanilla templates (mirrors the docker-compose :ro mounts).
COPY conf/vars.xml                               /usr/share/freeswitch/conf/vanilla/vars.xml
COPY conf/dialplan/default.xml                   /usr/share/freeswitch/conf/vanilla/dialplan/default.xml
COPY conf/dialplan/public.xml                     /usr/share/freeswitch/conf/vanilla/dialplan/public.xml
COPY conf/dialplan/whatsapp.xml                  /usr/share/freeswitch/conf/vanilla/dialplan/whatsapp.xml
COPY conf/directory/default.xml                  /usr/share/freeswitch/conf/vanilla/directory/default.xml
COPY conf/autoload_configs/modules.conf.xml      /usr/share/freeswitch/conf/vanilla/autoload_configs/modules.conf.xml
COPY conf/autoload_configs/acl.conf.xml          /usr/share/freeswitch/conf/vanilla/autoload_configs/acl.conf.xml
COPY conf/autoload_configs/event_socket.conf.xml /usr/share/freeswitch/conf/vanilla/autoload_configs/event_socket.conf.xml
COPY conf/autoload_configs/xml_curl.conf.xml     /usr/share/freeswitch/conf/vanilla/autoload_configs/xml_curl.conf.xml
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

# Dynamic trunk dir stays a runtime volume — just ensure the (empty) path exists.
RUN mkdir -p /usr/share/freeswitch/conf/vanilla/sip_profiles/external/dynamic \
 && mkdir -p /etc/freeswitch/sip_profiles/external/dynamic

# Recordings spool: seed the volume world-writable (sticky) so the NON-ROOT
# distroless services (gateway: media-plane recordings; orchestrator: S3 upload
# + cleanup) can write alongside root-owned FreeSWITCH recordings. A named
# volume inherits this dir's mode on first creation.
RUN mkdir -p /usr/share/freeswitch/recordings/tring \
    && chmod 1777 /usr/share/freeswitch/recordings/tring
