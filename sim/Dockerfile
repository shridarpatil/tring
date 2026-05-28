# SIP simulator — runs SIPp in UAS mode as a stand-in for the Tata SBC.
# Small Debian-based image; SIPp ships in the `sip-tester` package.
FROM debian:bookworm-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends sip-tester \
 && rm -rf /var/lib/apt/lists/*

COPY sipp-uas.xml /sipp-uas.xml

# UAS on UDP/5070, RTP from this same host. -trace_msg/-trace_err keep
# the SIP I/O visible in `docker logs` for debugging.
ENTRYPOINT ["sipp"]
CMD ["-sf", "/sipp-uas.xml", \
     "-i", "127.0.0.1", \
     "-p", "5070", \
     "-mi", "127.0.0.1"]
