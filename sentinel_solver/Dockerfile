# sentinel 求解服务：常驻一个无头 Chromium 的 chatgpt.com 页面。
# 与主服务分开部署（独立容器/服务），主服务通过 SENTINEL_SOLVER_URL 调用它。
FROM node:22-bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive \
    PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
    NODE_ENV=production \
    PORT=8080

WORKDIR /app

COPY package.json ./
RUN npm install --omit=dev --no-audit --no-fund

# channel="chromium" 需要完整 Chromium（不是 headless shell）
RUN npx playwright install --with-deps chromium
# Chromium 不支持带用户名密码的 SOCKS5 代理，用 gost 把这种上游转发成本机无认证的 SOCKS5
ARG GOST_VERSION=2.11.5
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends curl ca-certificates; \
    rm -rf /var/lib/apt/lists/*; \
    curl -fsSL -o /tmp/gost.gz "https://github.com/ginuerzh/gost/releases/download/v${GOST_VERSION}/gost-linux-amd64-${GOST_VERSION}.gz"; \
    gunzip -c /tmp/gost.gz > /usr/local/bin/gost; \
    chmod +x /usr/local/bin/gost; \
    rm -f /tmp/gost.gz; \
    gost -V

COPY vm.js server.js ./

EXPOSE 8080

CMD ["node", "server.js"]
