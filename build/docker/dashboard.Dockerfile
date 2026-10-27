FROM node:24.21.0-bookworm-slim AS builder
WORKDIR /app
ENV NEXT_TELEMETRY_DISABLED=1
RUN corepack enable && corepack prepare pnpm@10.33.4 --activate
COPY chill-dashboard/package.json chill-dashboard/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY chill-dashboard/ ./
RUN pnpm build
RUN mkdir -p /runtime/node_modules && cp -rL node_modules/ws /runtime/node_modules/ws

FROM node:24.21.0-bookworm-slim
WORKDIR /app
ENV NODE_ENV=production NEXT_TELEMETRY_DISABLED=1 HOSTNAME=0.0.0.0 PORT=3000
COPY --from=builder --chown=node:node /app/.next/standalone ./
COPY --from=builder --chown=node:node /app/.next/static ./.next/static
COPY --from=builder --chown=node:node /app/public ./public
COPY --from=builder /app/LICENSE.shadcn ./LICENSE.shadcn
COPY --from=builder --chown=node:node /app/server ./server
COPY --from=builder --chown=node:node /runtime/node_modules/ws ./node_modules/ws
USER node
EXPOSE 3000
CMD ["node", "server/live-proxy.mjs"]
