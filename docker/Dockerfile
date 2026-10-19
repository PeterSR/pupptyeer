# pupptyeer image: the daemon + CLI, pupptyeer-mcp, an entrypoint helper and a
# static tini.
#
# The binaries are NOT built here. goreleaser (and `make docker`) put the
# freshly built static ones in the build context at <os>/<arch>/<binary>,
# because release assets are drafts while the image builds. The context also
# holds docker/pupptyeer-entrypoint.sh, at its repo path (goreleaser keeps
# extra_files at their repo path).
#
# Everything the image ships lives in /opt/pupptyeer/bin as real,
# self-contained files, so a consumer on another base can `COPY --from=` that
# directory.
#
# Base: debian slim, not busybox. The binaries are static and would run on
# either, but ptys need a real shell and coreutils (bash, ls, ...) to be
# useful, tini-static is one apt package away, and consumers are mostly
# Debian-ish. The ~75MB base does not matter for COPY --from, which takes only
# /opt/pupptyeer/bin.
#
# Needs BuildKit (COPY --chmod), which buildx and current `docker build` use.

ARG DEBIAN_TAG=bookworm-slim
FROM debian:${DEBIAN_TAG}

ARG TARGETPLATFORM
ARG PUPPTYEER_VERSION=dev
ARG REVISION=unknown
ARG PUPPTYEER_UID=1000
ARG PUPPTYEER_GID=1000

LABEL org.opencontainers.image.title="pupptyeer" \
      org.opencontainers.image.description="pupptyeer daemon, CLI and MCP server (static binaries)" \
      org.opencontainers.image.source="https://github.com/PeterSR/pupptyeer" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${PUPPTYEER_VERSION}" \
      org.opencontainers.image.revision="${REVISION}"

# tini: its static build is copied into /opt/pupptyeer/bin, so it runs on any
# base. HOME is world-writable and sticky (like /tmp), so `docker run --user
# <uid>` with no passwd entry can still write its config and state there.
RUN apt-get update \
 && apt-get install -y --no-install-recommends tini \
 && rm -rf /var/lib/apt/lists/* \
 && groupadd --gid "${PUPPTYEER_GID}" pupptyeer \
 && useradd --uid "${PUPPTYEER_UID}" --gid "${PUPPTYEER_GID}" --create-home --home-dir /home/pupptyeer --shell /bin/bash pupptyeer \
 && chmod 1777 /home/pupptyeer \
 && mkdir -p /opt/pupptyeer/bin \
 && cp /usr/bin/tini-static /opt/pupptyeer/bin/tini \
 && chmod 0755 /opt/pupptyeer/bin/tini

COPY --chown=0:0 --chmod=0755 ${TARGETPLATFORM}/pupptyeer ${TARGETPLATFORM}/pupptyeer-mcp /opt/pupptyeer/bin/
COPY --chown=0:0 --chmod=0755 docker/pupptyeer-entrypoint.sh /opt/pupptyeer/bin/pupptyeer-entrypoint

ENV HOME=/home/pupptyeer \
    PATH=/opt/pupptyeer/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    PUPPTYEER_SOCK=/tmp/pupptyeer/daemon.sock

USER pupptyeer
WORKDIR /home/pupptyeer
ENTRYPOINT ["/opt/pupptyeer/bin/tini", "-g", "--", "/opt/pupptyeer/bin/pupptyeer-entrypoint"]
CMD ["sh"]
