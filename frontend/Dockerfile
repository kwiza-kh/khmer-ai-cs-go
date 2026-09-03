# syntax=docker/dockerfile:1
# Frontend — multi-stage build for Next.js (standalone output).
#
# Stage 1 (deps):  install node_modules (cached layer, only rebuilds when
#                  package-lock.json changes).
# Stage 2 (build): run `next build`, producing .next/standalone + .next/static.
# Stage 3 (runtime): copy only the standalone server + static assets into a
#                  minimal alpine image. No node_modules, no source code.

# ---------- Stage 1: deps ----------
FROM node:20-alpine AS deps
WORKDIR /app
COPY package.json package-lock.json ./
# `npm ci` is the reproducible install (faster + safer than install in CI).
RUN npm ci

# ---------- Stage 2: build ----------
FROM node:20-alpine AS builder
WORKDIR /app
COPY --from=deps /app/node_modules ./node_modules
COPY . .
# Build-time env: the browser-visible API URL. Default points at the backend
# container's published port; override with --build-arg for production domains.
ARG NEXT_PUBLIC_API_URL=http://localhost:8080/api/v1
ENV NEXT_PUBLIC_API_URL=$NEXT_PUBLIC_API_URL
RUN npm run build

# ---------- Stage 3: runtime ----------
FROM node:20-alpine AS runtime
WORKDIR /app

ENV NODE_ENV=production
# Next.js standalone server listens on this port.
ENV PORT=3000
ENV HOSTNAME=0.0.0.0

# Non-root user.
RUN addgroup -g 10001 -S nodejs && adduser -S nextjs -u 10001 -G nodejs

# Next.js standalone mirrors the build's absolute path inside .next/standalone
# (e.g. .next/standalone/<abs/path>/server.js together with a node_modules/).
# Copy the whole standalone tree, then locate the TOP-LEVEL server.js (skip
# anything inside node_modules/) and flatten its directory into /app.
COPY --from=builder --chown=nextjs:nodejs /app/.next/standalone /standalone
RUN SERVER_DIR=$(dirname "$(find /standalone -name server.js -type f \
        -not -path '*/node_modules/*' | head -1)") && \
    if [ -z "$SERVER_DIR" ]; then echo "server.js not found in standalone"; exit 1; fi && \
    echo "Standalone root: $SERVER_DIR" && \
    cp -a "$SERVER_DIR/." /app/ && \
    rm -rf /standalone

# Static assets + public/ are not included in standalone, must be copied separately.
COPY --from=builder --chown=nextjs:nodejs /app/.next/static ./.next/static
COPY --from=builder --chown=nextjs:nodejs /app/public ./public

USER nextjs
EXPOSE 3000

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD wget -qO- http://127.0.0.1:3000/ >/dev/null || exit 1

CMD ["node", "server.js"]
