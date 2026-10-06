# The portal image: the built single-page app behind an unprivileged nginx.
#
# ONE stage and no RUN line. The page is built OUTSIDE the image
# (`yarn --cwd apps/portal build`, a goreleaser `before` hook), so nothing
# executes while the image is assembled and it builds for another
# architecture without emulation.
#
# The COPY paths are from the REPOSITORY ROOT: GoReleaser stages each
# `extra_files` entry into the build context keeping its path.
#
# The base is pinned by tag and digest; renovate moves both.
FROM nginxinc/nginx-unprivileged:1.31.6-alpine@sha256:b9241c6e7b8e9a862f129d8d4199ab64b10390949a78bdd5603379b32c844083

COPY apps/portal/dist /usr/share/nginx/html
COPY apps/portal/server/default.conf /etc/nginx/conf.d/default.conf

LABEL org.opencontainers.image.title="portal" \
      org.opencontainers.image.source="https://github.com/truvity/observability"

EXPOSE 8080
