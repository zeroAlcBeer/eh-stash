# ── Stage 1: build ──────────────────────────────────────────────
FROM node:22-alpine AS builder
ENV PNPM_HOME="/pnpm"
ENV PATH="$PNPM_HOME:$PATH"
RUN corepack enable

WORKDIR /app
COPY package.json ./
RUN pnpm install

COPY . .
# VITE_THUMB_BASE_URL: bake the R2 thumb CDN origin into the self-hosted build
# so the frontend fetches thumbs directly from R2 (same as ehstash.com).
# Empty (dev build) ⇒ falls back to same-origin /v1/thumbs/{gid}.
ARG VITE_THUMB_BASE_URL=""
ENV VITE_THUMB_BASE_URL=${VITE_THUMB_BASE_URL}
RUN pnpm build

# ── Stage 2: preview (serve dist/ via vite preview) ──────────────
FROM node:22-alpine AS runner
ENV PNPM_HOME="/pnpm"
ENV PATH="$PNPM_HOME:$PATH"
RUN corepack enable

WORKDIR /app
# 直接复用 builder 的 node_modules（含 vite），无需重装
COPY --from=builder /app/node_modules ./node_modules
COPY --from=builder /app/dist ./dist
COPY package.json vite.config.js ./

EXPOSE 4173
CMD ["pnpm", "exec", "vite", "preview", "--host", "--port", "4173"]
