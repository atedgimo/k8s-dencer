# Frontend image: Vite build served by nginx-unprivileged.
#
# nginx-unprivileged is used rather than stock nginx so the container runs as a
# non-root user and binds an unprivileged port, which the chart's restricted
# securityContext requires.
#
# syntax=docker/dockerfile:1

ARG NODE_VERSION=22

FROM --platform=$BUILDPLATFORM node:${NODE_VERSION}-alpine AS build

WORKDIR /src

COPY ui/package.json ui/package-lock.json* ./
RUN npm ci

COPY ui/ ./
RUN npm run build

FROM nginxinc/nginx-unprivileged:1.31-alpine

COPY --from=build /src/dist /usr/share/nginx/html
COPY ui/nginx.conf /etc/nginx/conf.d/default.conf

# The API base URL is injected at pod start from a ConfigMap mounted over this
# file. Baking it into the bundle would tie the image to one cluster.
COPY ui/public/config.js /usr/share/nginx/html/config.js

USER 101
EXPOSE 8080
