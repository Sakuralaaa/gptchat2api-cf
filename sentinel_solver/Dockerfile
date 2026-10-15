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

COPY vm.js server.js ./

EXPOSE 8080

CMD ["node", "server.js"]
