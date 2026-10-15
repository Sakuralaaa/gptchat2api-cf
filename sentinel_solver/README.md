# sentinel 求解服务

从 2026-10-14 起，chatgpt.com 的对话接口（文字和生图）开始严格校验 sentinel：

- `chat-requirements` 必须带上在**真实 chatgpt.com 页面**里算出的 Turnstile token。纯 Python 或 Node 模拟算出的结果会被拒，缺失时返回 403 `Unusual activity`。
- requirements/proof token 里编码的 **UA 必须和算 Turnstile 的页面一致**。
- 页面时区应与出口 IP 所在地一致。
- 只要校验质量不够，对话接口不会直接返回 403，而是返回伪装成 404 的 `{"detail":"Model not found"}`。

本服务常驻一个无头 Chromium，一直开着 chatgpt.com 页面，替主服务计算 Turnstile。主服务不需要打包浏览器。

## 接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/health` | 页面状态、页面使用的 UA 与页面时区（主服务据此生成 requirements/proof token 并对齐时区） |
| POST | `/v1/turnstile` | `{"dx","p"}` → `{"token"}`，dx 取自 prepare 响应，p 是 prepare 请求提交的 requirements token |
| POST | `/v1/so` | `{"collector_dx","snapshot_dx","p"}` → `{"token"}`，目前对话接口还不强制要求 |

设置了 `SENTINEL_SOLVER_KEY` 时，请求需要带 `Authorization: Bearer <key>` 头。

## 部署

```bash
docker build -t sentinel-solver ./sentinel_solver
docker run -d -p 8080:8080 \
  -e SENTINEL_SOLVER_KEY=<key> \
  -e SENTINEL_SOLVER_PROXY=<与主服务相同的出口代理> \
  -e SENTINEL_SOLVER_TIMEZONE=<与出口 IP 一致，如 America/Phoenix> \
  sentinel-solver
```

主服务通过设置项（或 `CHATGPT2API_SENTINEL_SOLVER_URL` / `CHATGPT2API_SENTINEL_SOLVER_KEY` 环境变量）指向本服务：

```
CHATGPT2API_SENTINEL_SOLVER_URL=http://sentinel-solver:8080
CHATGPT2API_SENTINEL_SOLVER_KEY=<与 SENTINEL_SOLVER_KEY 相同>
```

未配置或调用失败时，主服务会退回本地 Turnstile 求解器；本地求解器已跟不上新版挑战程序，对话接口大概率会失败。

## 环境变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `SENTINEL_SOLVER_KEY` | 空 | 访问密钥 |
| `SENTINEL_SOLVER_PROXY` | 空 | 浏览器出网代理，应与主服务访问 chatgpt.com 的出口一致 |
| `SENTINEL_SOLVER_LOCAL_PROXY_PORT` | 1080 | 上游是带认证的 SOCKS5 时，本机无认证转发端口 |
| `SENTINEL_SOLVER_USER_AGENT` | 按内置 Chromium 版本生成 | 页面 UA |
| `SENTINEL_SOLVER_TIMEZONE` | 跟随系统 | 页面时区（如 `America/Phoenix`），应与出口 IP 所在地一致 |
| `SENTINEL_SOLVER_REFRESH_SECONDS` | 1800 | 页面定期重开的间隔 |
| `SENTINEL_SOLVER_LOCALE` | `en-US` | 页面 locale（决定 `navigator.language`） |
| `SENTINEL_SOLVER_HEADLESS` | true | 调试时可设为 false |
| `SENTINEL_SOLVER_TARGET` | https://chatgpt.com/ | 常驻页面地址 |
| `PORT` | 8080 | 监听端口 |

## 注意

- 页面时区与 `getTimezoneOffset()` 都从**真实页面**读取（`/health` 返回 `timezone` / `timezone_offset_min` / `timezone_long_name` / `locale`），夏令时切换会自动跟随，不需要写死偏移值。
- `SENTINEL_SOLVER_PROXY` 支持带认证信息（`http://user:pass@host:port`），用户名密码会拆出来传给 Chromium。
- Chromium 不支持带用户名密码的 SOCKS5 代理：检测到这种上游时，本服务会用内置的 gost 在本机起一个无认证 SOCKS5 转发，Chromium 只连 `127.0.0.1`（端口见 `SENTINEL_SOLVER_LOCAL_PROXY_PORT`）。
- 本服务的出口是**固定**的（不随账号动态切换）。同一个代理供应商/主机不代表同一个出口，代理账号里的 session 不同也可能分配到不同 IP，因此无法保证整个号池都与同一出口一致。

- 必须使用完整 Chromium 的新版无头模式（`channel="chromium"`）；默认的 headless shell 会停在 Cloudflare「请稍候…」，算出的 token 无效。
- 挑战程序（`vm.js`）移植自 chatgpt.com 前端 bundle（2026-10 版本，指令编号 0-35），OpenAI 更新指令集后需要同步更新。
- 本服务只解决**请求质量**问题。由脚本批量注册的账号，即使在真实网页里也可能提示 `Model not found`，属于账号层面的限制。
