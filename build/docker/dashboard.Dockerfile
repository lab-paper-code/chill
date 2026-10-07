FROM node:24.21.0-bookworm-slim AS builder
WORKDIR /app
ENV NEXT_TELEMETRY_DISABLED=1
RUN corepack enable && corepack prepare pnpm@10.33.4 --activate
COPY chill-dashboard/package.json chill-dashboard/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY chill-dashboard/ ./
RUN pnpm build

FROM node:24.21.0-bookworm-slim
WORKDIR /app
ENV NODE_ENV=production NEXT_TELEMETRY_DISABLED=1 HOSTNAME=0.0.0.0 PORT=3000
COPY --from=builder --chown=node:node /app/.next/standalone ./
COPY --from=builder --chown=node:node /app/.next/static ./.next/static
COPY --from=builder --chown=node:node /app/public ./public
COPY --from=builder /app/LICENSE.shadcn ./LICENSE.shadcn
USER node
EXPOSE 3000
CMD ["node", "server.js"]
