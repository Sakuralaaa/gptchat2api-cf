'use strict';
// sentinel 求解服务：常驻一个无头 Chromium 的 chatgpt.com 页面，替主服务计算 Turnstile / SO token。
//
// 2026-10-14 起 chatgpt.com 的对话接口要求 Turnstile token 必须在真实的 chatgpt.com
// 页面里算出，纯 Node/Python 模拟的结果会被拒。主服务不打包浏览器，改为通过 HTTP 调用本服务。
//
// 环境变量：
// - SENTINEL_SOLVER_KEY：可选，设置后请求需带 `Authorization: Bearer <key>`
// - SENTINEL_SOLVER_PROXY：可选，浏览器出网代理（如 http://host:7890），应与主服务出口一致
// - SENTINEL_SOLVER_USER_AGENT：可选，默认按内置 Chromium 版本生成 Linux Chrome UA
// - SENTINEL_SOLVER_TIMEZONE：可选，页面时区（如 America/Phoenix），应与出口 IP 所在地一致
// - SENTINEL_SOLVER_REFRESH_SECONDS：页面定期重开的间隔，默认 1800
// - SENTINEL_SOLVER_HEADLESS：默认 true；调试时可设为 false
// - SENTINEL_SOLVER_TARGET：默认 https://chatgpt.com/
// - PORT：监听端口，默认 8080

const http = require('http');
const crypto = require('crypto');
const fs = require('fs');
const path = require('path');
const { chromium } = require('playwright');
const { spawn } = require('child_process');

const CHATGPT_URL = process.env.SENTINEL_SOLVER_TARGET || 'https://chatgpt.com/';
const CHALLENGE_TITLES = ['请稍候', 'Just a moment', 'Attention Required'];
const VM_SCRIPT = fs.readFileSync(path.join(__dirname, 'vm.js'), 'utf8');
const BASE_INIT_SCRIPT = `
Object.defineProperty(navigator, 'webdriver', { get: () => undefined });
window.chrome = window.chrome || { runtime: {} };
Object.defineProperty(navigator, 'languages', { get: () => ['zh-CN', 'zh', 'en'] });
Object.defineProperty(navigator, 'plugins', { get: () => [1, 2, 3, 4, 5] });
(() => {
  const original = navigator.permissions && navigator.permissions.query;
  if (!original) return;
  navigator.permissions.query = (parameters) => (
    parameters && parameters.name === 'notifications'
      ? Promise.resolve({ state: Notification.permission })
      : original(parameters)
  );
})();
`;

const SOLVER_KEY = (process.env.SENTINEL_SOLVER_KEY || '').trim();
const PROXY = (process.env.SENTINEL_SOLVER_PROXY || '').trim();
const USER_AGENT = (process.env.SENTINEL_SOLVER_USER_AGENT || '').trim();
const TIMEZONE = (process.env.SENTINEL_SOLVER_TIMEZONE || '').trim();

const LOCALE = (process.env.SENTINEL_SOLVER_LOCALE || 'en-US').trim();
const PROXY_OPTIONS = proxyOptionsFromURL(PROXY);
const LOCAL_PROXY_PORT = parseInt(process.env.SENTINEL_SOLVER_LOCAL_PROXY_PORT || '1080', 10) || 1080;
const LOCAL_PROXY_OPTIONS = needsLocalProxy(PROXY_OPTIONS) ? { server: `socks5://127.0.0.1:${LOCAL_PROXY_PORT}` } : null;

/** 把 http(s)/socks 代理 URL 拆成 playwright 的 {server, username, password}。 */
function proxyOptionsFromURL(raw) {
  const value = (raw || '').trim();
  if (!value) return null;
  try {
    const url = new URL(value);
    const options = { server: `${url.protocol}//${url.host}` };
    if (url.username) options.username = decodeURIComponent(url.username);
    if (url.password) options.password = decodeURIComponent(url.password);
    return options;
  } catch (err) {
    console.log('[sentinel-solver] invalid SENTINEL_SOLVER_PROXY, using the raw value');
    return { server: value };
  }
}

/** Chromium 不能为 SOCKS5 上游做代理认证，这类上游必须在本机先转发一层。 */
function needsLocalProxy(options) {
  if (!options) return false;
  if (!/^socks/i.test(String(options.server || ''))) return false;
  return Boolean(options.username || options.password);
}

let localProxyProcess = null;

function waitForLocalProxy(port, timeoutMs) {
  const net = require('net');
  const deadline = Date.now() + timeoutMs;
  return new Promise((resolve, reject) => {
    const attempt = () => {
      const socket = net.connect(port, '127.0.0.1');
      socket.once('connect', () => {
        socket.destroy();
        resolve();
      });
      socket.once('error', () => {
        socket.destroy();
        if (Date.now() > deadline) {
          reject(new Error(`local proxy 127.0.0.1:${port} not ready`));
          return;
        }
        setTimeout(attempt, 300);
      });
    };
    attempt();
  });
}

/** 用 gost 把带认证的 SOCKS5 上游转发成本机无认证的 SOCKS5（Chromium 只连本机端口）。 */
async function startLocalProxy() {
  if (!LOCAL_PROXY_OPTIONS || localProxyProcess) return;
  const child = spawn('gost', ['-L', LOCAL_PROXY_OPTIONS.server, '-F', PROXY], { stdio: ['ignore', 'pipe', 'pipe'] });
  child.stdout.on('data', (chunk) => log('gost:', String(chunk).trim()));
  child.stderr.on('data', (chunk) => log('gost:', String(chunk).trim()));
  child.on('exit', (code) => {
    log('gost exited with code', code);
    localProxyProcess = null;
  });
  localProxyProcess = child;
  await waitForLocalProxy(LOCAL_PROXY_PORT, 15000);
  log(`local auth-free socks5 ready on 127.0.0.1:${LOCAL_PROXY_PORT}`);
}

function stopLocalProxy() {
  if (!localProxyProcess) return;
  try { localProxyProcess.kill('SIGTERM'); } catch (err) { /* ignore */ }
  localProxyProcess = null;
}

/** 从真实页面读取时区/偏移/locale；偏移值随夏令时变化，不能写死。 */
async function readPageProfile(page) {
  try {
    const profile = await page.evaluate(() => ({
      timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
      timezone_offset_min: new Date().getTimezoneOffset(),
      locale: navigator.language,
    }));
    if (!profile || !profile.timezone) return null;
    profile.timezone_long_name = timezoneLongName(profile.timezone);
    return profile;
  } catch (err) {
    log('read page profile failed:', errText(err));
    return null;
  }
}
const REFRESH_SECONDS = Math.max(60, parseInt(process.env.SENTINEL_SOLVER_REFRESH_SECONDS || '1800', 10) || 1800);
const HEADLESS = !['0', 'false', 'no'].includes((process.env.SENTINEL_SOLVER_HEADLESS || 'true').trim().toLowerCase());
const READY_TIMEOUT_MS = 60000;
// 页面自带的 turnstile 模块首次使用要 fetch/parse，给它留足时间；页面打开时已预热。
const SDK_SOLVE_TIMEOUT_MS = 45000;
const VM_SOLVE_TIMEOUT_MS = 20000;
const PORT = parseInt(process.env.PORT || '8080', 10) || 8080;

const log = (...args) => console.log('[sentinel-solver]', new Date().toISOString(), ...args);
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const errText = (err) => String((err && err.message) || err);

function withTimeout(promise, ms, message) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(message)), ms);
    promise.then(
      (value) => { clearTimeout(timer); resolve(value); },
      (err) => { clearTimeout(timer); reject(err); }
    );
  });
}

// 首选路径：直接 import chatgpt.com 页面自己加载的 turnstile chunk，在真实页面环境里调
// getEnforcementToken。手写 VM 只能作回退 —— 它得跟着前端版本走，而调用官方模块不用。
// chunk 文件名带内容哈希、每次发版都变，所以求解时动态发现，不写死。
const PINNED_TURNSTILE_ASSET = '/unauth-mweb/assets/turnstile-DykPLDEM.js';
// 注意：下面这些必须是真正的函数，不能是字符串。page.evaluate(string, arg) 只把字符串当
// 表达式求值并忽略第二个参数，而 `() => {...}` 求值出来是个函数、无法序列化，于是永远返回
// undefined —— 这正是 solver 一直回 {"token":""} 的原因。
const discoverTurnstileAssets = () => {
  const re = /\/turnstile-[^/"'\s]+?\.js/g;
  const seen = [];
  const add = (value) => {
    if (!value) return;
    const hits = String(value).match(re);
    if (hits) hits.forEach((hit) => { if (!seen.includes(hit)) seen.push(hit); });
  };
  try { performance.getEntriesByType('resource').forEach((e) => add(e.name)); } catch (err) {}
  try { Array.from(document.scripts).forEach((s) => { add(s.src); add(s.textContent); }); } catch (err) {}
  try { Array.from(document.querySelectorAll('link[href]')).forEach((l) => add(l.href)); } catch (err) {}
  try { add(document.documentElement ? document.documentElement.outerHTML : ''); } catch (err) {}
  return seen;
};

const scanScriptsForTurnstileAsset = async (pattern) => {
  const re = new RegExp(pattern);
  for (const script of Array.from(document.scripts)) {
    if (!script.src) continue;
    try {
      const text = await (await fetch(script.src)).text();
      const hit = text.match(re);
      if (hit) return hit[0];
    } catch (err) {}
  }
  return null;
};

const solveTurnstileViaSdk = async ([requirements, key, asset]) => {
  try {
    const mod = await import(asset);
    const turnstile = mod.turnstile || (mod.default && mod.default.turnstile);
    if (!turnstile || typeof turnstile.getEnforcementToken !== 'function') {
      return 'ERR:getEnforcementToken missing';
    }
    try {
      window.__solverSdkKeys = { mod: Object.keys(mod), turnstile: Object.keys(turnstile) };
    } catch (err) { /* ignore */ }
    // 这个导出按 requirements 对象查 WeakMap，同一个对象会被查两次，含义不同：
    //   第 1 次 =「这个 requirements 算过了吗」的缓存判断。命中就直接返回缓存值，
    //           所以必须先让它 miss（返回 undefined），否则拿回来的是缓存里的旧值。
    //   之后的 = 挑战程序解码 dx 用的 XOR key（前端叫 cachedProof，就是 prepare 请求里的 p）。
    // 上游那版把两次都命中了，于是返回的一直是 p 本身。
    const original = WeakMap.prototype.get;
    let lookups = 0;
    WeakMap.prototype.get = function (target) {
      if (target !== requirements) return original.call(this, target);
      lookups += 1;
      return lookups === 1 ? undefined : key;
    };
    try {
      const token = await turnstile.getEnforcementToken(requirements);
      try {
        window.__solverSdkLookups = lookups;
      } catch (err) { /* ignore */ }
      return String(token);
    } finally {
      WeakMap.prototype.get = original;
    }
  } catch (error) {
    return 'ERR:' + String(error).slice(0, 300);
  }
};

const solveTurnstileViaVm = ([dx, p]) => window.__sentinelVM.turnstile(dx, p);

const verifyTurnstileAssetInPage = async (asset) => {
  try {
    const r = await fetch(asset, { method: 'GET' });
    const text = await r.text();
    return { status: r.status, ok: r.ok, bytes: text.length, looksLikeModule: /turnstile|getEnforcementToken|export/i.test(text) };
  } catch (err) {
    return { error: String(err).slice(0, 200) };
  }
};

const warmTurnstileAssetInPage = async (asset) => {
  try {
    const mod = await import(asset);
    return !!(mod.turnstile || (mod.default && mod.default.turnstile));
  } catch (err) {
    return false;
  }
};

const pageDebugState = async () => ({
  title: document.title,
  host: location.hostname,
  readyState: document.readyState,
  vm: typeof window.__sentinelVM,
  vmTurnstile: window.__sentinelVM ? typeof window.__sentinelVM.turnstile : 'n/a',
  scriptSrcs: Array.from(document.scripts).map((s) => s.src).filter(Boolean),
  turnstileish: performance.getEntriesByType('resource').map((e) => e.name).filter((n) => /turnstile|assets\//i.test(n)),
  htmlTurnstile: (document.documentElement ? document.documentElement.outerHTML : '').match(/\/unauth-mweb\/assets\/turnstile-[A-Za-z0-9_-]+\.js/g),
  sdkKeys: window.__solverSdkKeys || null,
  sdkLookups: typeof window.__solverSdkLookups === "number" ? window.__solverSdkLookups : null,
});

/** JS getTimezoneOffset() 语义：本地时间到 UTC 的分钟差（西半球为正）。 */
function timezoneOffsetMinutes(timeZone) {
  try {
    const now = new Date();
    const parts = new Intl.DateTimeFormat('en-US', {
      timeZone,
      hour12: false,
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
    }).formatToParts(now);
    const value = (type) => Number(parts.find((part) => part.type === type).value);
    const asUTC = Date.UTC(value('year'), value('month') - 1, value('day'), value('hour') % 24, value('minute'), value('second'));
    return Math.round((Math.floor(now.getTime() / 1000) * 1000 - asUTC) / 60000);
  } catch (err) {
    return null;
  }
}

/** 形如 "Mountain Standard Time"；供主服务拼 requirements token 里的时间串。 */
function timezoneLongName(timeZone) {
  try {
    const part = new Intl.DateTimeFormat('en-US', { timeZone, timeZoneName: 'long' })
      .formatToParts(new Date())
      .find((item) => item.type === 'timeZoneName');
    return part ? part.value : null;
  } catch (err) {
    return null;
  }
}

class SolverPage {
  /** 维护一个已通过 Cloudflare 校验的 chatgpt.com 页面，按需重开。 */
  constructor() {
    this.browser = null;
    this.context = null;
    this.page = null;
    this.openedAt = 0;
    this.userAgent = USER_AGENT;
    this.lastError = '';
    this.profile = null;
    this.turnstileAssetPath = '';
    this.lastSolve = null;
    this.lock = Promise.resolve();
  }

  async start() {
    const launch = {
      headless: HEADLESS,
      // channel='chromium' 使用完整 Chromium 的新版无头模式；默认的 headless shell 过不了 Cloudflare
      channel: 'chromium',
      args: ['--disable-blink-features=AutomationControlled', '--no-sandbox', '--disable-dev-shm-usage'],
    };
    const launchProxy = LOCAL_PROXY_OPTIONS || PROXY_OPTIONS;
    if (launchProxy) launch.proxy = launchProxy;
    this.browser = await chromium.launch(launch);
    if (!this.userAgent) {
      const major = String(this.browser.version() || '145').split('.')[0];
      this.userAgent =
        'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 ' +
        `(KHTML, like Gecko) Chrome/${major}.0.0.0 Safari/537.36`;
    }
    log('browser launched', JSON.stringify({ version: this.browser.version(), user_agent: this.userAgent, timezone: TIMEZONE || null, proxy: PROXY ? '(set)' : null }));
    try {
      await this.ensurePage();
      // 页面一就绪就发现并 import 一次页面自带的 turnstile chunk，首个求解请求不用等它下载解析。
      await this.resolveTurnstileAsset();
    } catch (err) {
      // 启动时页面暂时打不开不致命，首个请求会重试
      this.lastError = errText(err);
      log('initial page open failed:', this.lastError);
    }
  }

  async stop() {
    try { if (this.context) await this.context.close(); } catch (err) { /* ignore */ }
    try { if (this.browser) await this.browser.close(); } catch (err) { /* ignore */ }
    stopLocalProxy();
  }

  /** 串行化重开，避免并发请求各开一个页面。 */
  withLock(fn) {
    const run = this.lock.then(fn, fn);
    this.lock = run.then(() => undefined, () => undefined);
    return run;
  }

  async ensurePage() {
    return this.withLock(async () => {
      let stale = !this.page || Date.now() / 1000 - this.openedAt > REFRESH_SECONDS;
      if (!stale && !(await this.isReady(this.page))) stale = true;
      if (stale) await this.reopen();
      return this.page;
    });
  }

  async invalidate() {
    await this.withLock(async () => { this.openedAt = 0; });
  }

  async reopen() {
    if (this.context) {
      try { await this.context.close(); } catch (err) { /* ignore */ }
    }
    this.page = null;
    const contextOptions = {
      locale: LOCALE,
      viewport: { width: 1280, height: 800 },
      userAgent: this.userAgent,
    };
    if (TIMEZONE) contextOptions.timezoneId = TIMEZONE;
    const context = await this.browser.newContext(contextOptions);
    await context.addInitScript(BASE_INIT_SCRIPT);
    await context.addInitScript(VM_SCRIPT);
    const page = await context.newPage();
    await page.goto(CHATGPT_URL, { waitUntil: 'domcontentloaded', timeout: 60000 });
    const deadline = Date.now() + READY_TIMEOUT_MS;
    while (!(await this.isReady(page))) {
      if (Date.now() > deadline) {
        let title = '';
        try { title = await page.title(); } catch (err) { /* ignore */ }
        try { await context.close(); } catch (err) { /* ignore */ }
        throw new Error(`chatgpt.com page not ready (title=${JSON.stringify(title)}), likely stuck on a Cloudflare challenge`);
      }
      await sleep(1000);
    }
    this.context = context;
    this.page = page;
    this.openedAt = Date.now() / 1000;
    this.lastError = '';
    this.profile = await readPageProfile(page);
    log('chatgpt.com page ready:', JSON.stringify(await page.title()));
  }

  async isReady(page) {
    if (!page || page.isClosed()) return false;
    try {
      const title = await page.title();
      if (CHALLENGE_TITLES.some((marker) => title.includes(marker))) return false;
      return await page.evaluate(
        () => location.hostname === 'chatgpt.com' && typeof window.__sentinelVM === 'object'
      );
    } catch (err) {
      return false;
    }
  }

  /** 在页面里执行；页面失效时重开一次再试。timeoutMs > 0 时给单次调用加超时。 */
  async evaluate(expression, arg, timeoutMs) {
    for (let attempt = 0; attempt < 2; attempt += 1) {
      const page = await this.ensurePage();
      try {
        const result = page.evaluate(expression, arg);
        if (timeoutMs > 0) {
          return await withTimeout(result, timeoutMs, `page.evaluate timed out after ${timeoutMs}ms`);
        }
        return await result;
      } catch (err) {
        this.lastError = errText(err);
        if (attempt) throw err;
        await this.invalidate();
      }
    }
    throw new Error('unreachable');
  }
  /** 定位 chatgpt.com 自己加载的 turnstile chunk：先看页面已加载资源，再扫页面脚本。 */
  async resolveTurnstileAsset() {
    if (this.turnstileAssetPath) return this.turnstileAssetPath;
    try {
      const loaded = await this.evaluate(discoverTurnstileAssets, null, SDK_SOLVE_TIMEOUT_MS);
      if (Array.isArray(loaded) && loaded.length) {
        this.turnstileAssetPath = String(loaded[0]);
        log('turnstile asset (loaded):', this.turnstileAssetPath);
        await this.warmTurnstileAsset(this.turnstileAssetPath);
        return this.turnstileAssetPath;
      }
      const scanned = await this.evaluate(
        scanScriptsForTurnstileAsset,
        '/unauth-mweb/assets/turnstile-[A-Za-z0-9_-]+\\.js',
        SDK_SOLVE_TIMEOUT_MS
      );
      if (scanned) {
        this.turnstileAssetPath = String(scanned);
        log('turnstile asset (scanned):', this.turnstileAssetPath);
        await this.warmTurnstileAsset(this.turnstileAssetPath);
        return this.turnstileAssetPath;
      }
    } catch (err) {
      this.lastError = errText(err);
      log('turnstile asset discovery error:', errText(err));
    }
    log('turnstile asset discovery fell back to', PINNED_TURNSTILE_ASSET);
    this.turnstileAssetPath = PINNED_TURNSTILE_ASSET;
    await this.warmTurnstileAsset(this.turnstileAssetPath);
    return this.turnstileAssetPath;
  }

  /** 提前 import 一次页面自带的 turnstile 模块，把它预热进模块缓存。 */
  async warmTurnstileAsset(asset) {
    try {
      const check = await this.evaluate(verifyTurnstileAssetInPage, asset, SDK_SOLVE_TIMEOUT_MS);
      log(`turnstile asset check ${JSON.stringify(check)}:`, asset);
      if (check && check.ok) {
        const ready = await this.evaluate(warmTurnstileAssetInPage, asset, SDK_SOLVE_TIMEOUT_MS);
        log(`turnstile asset warm-up ${ready ? 'ok' : 'failed'}:`, asset);
      }
    } catch (err) {
      this.lastError = errText(err);
      log('turnstile asset warm-up error:', errText(err));
    }
  }

  /**
   * 求解 Turnstile 挑战。
   *
   * 首选页面自带 turnstile chunk 的 `getEnforcementToken(requirements)`：它就在真实
   * chatgpt.com 页面里跑挑战程序，且不需要我们跟着前端版本维护一份 VM 移植。
   *
   * 回退：页面内 VM 移植（和 chunk 同一套指令，但版本漂移时可能算错）。
   */
  async solveTurnstile(requirements, dx, p) {
    const diag = { at: new Date().toISOString(), dxLen: String(dx || '').length, pLen: String(p || '').length, hasRequirements: !!requirements };
    this.lastSolve = diag;
    if (requirements && dx) {
      const asset = await this.resolveTurnstileAsset();
      diag.asset = asset;
      let result = '';
      try {
        const raw = await this.evaluate(solveTurnstileViaSdk, [requirements, p, asset], SDK_SOLVE_TIMEOUT_MS);
        diag.sdkType = typeof raw;
        result = String(raw == null ? '' : raw);
      } catch (err) {
        result = 'ERR:' + errText(err);
      }
      diag.sdkResult = result.slice(0, 300);
      diag.sdkLen = result.length;
      // 返回值等于 p 就说明缓存判断被命中、挑战程序没跑，不能当 token 用。
      if (result && !result.startsWith('ERR:') && result !== String(p)) {
        diag.via = 'sdk';
        return { ok: true, token: result };
      }
      this.lastError = result.slice(0, 300);
      log(`SDK turnstile solve failed via ${asset}: ${result.slice(0, 300)}`);
      this.turnstileAssetPath = '';
      await this.invalidate();
    }
    if (!dx) {
      diag.via = 'none';
      return { ok: false, token: this.lastError || 'no dx and no requirements supplied' };
    }
    let vm = '';
    try {
      const raw = await this.evaluate(solveTurnstileViaVm, [dx, p], VM_SOLVE_TIMEOUT_MS);
      diag.vmType = typeof raw;
      vm = String(raw == null ? '' : raw);
    } catch (err) {
      vm = 'ERR:' + errText(err);
    }
    diag.vmLen = vm.length;
    diag.vmResult = vm.slice(0, 300);
    diag.via = 'vm';
    if (vm.startsWith('ERR:')) return { ok: false, token: vm.slice(4) };
    return { ok: !!vm, token: vm };
  }

  /** Diagnostics for the live page: what it loaded and how the last solve went. */
  async debugState() {
    const out = {
      lastError: this.lastError || null,
      turnstileAssetPath: this.turnstileAssetPath || null,
      lastSolve: this.lastSolve || null,
    };
    try {
      out.page = await this.evaluate(pageDebugState, null, 20000);
    } catch (err) {
      out.pageError = errText(err);
    }
    return out;
  }

  status() {
    return {
      ready: !!this.page && !this.page.isClosed(),
      page_age_seconds: this.openedAt ? Math.floor(Date.now() / 1000 - this.openedAt) : null,
      browser_version: this.browser ? this.browser.version() : null,
      user_agent: this.userAgent,
      timezone: (this.profile && this.profile.timezone) || TIMEZONE || null,
      timezone_offset_min: this.profile ? this.profile.timezone_offset_min : (TIMEZONE ? timezoneOffsetMinutes(TIMEZONE) : null),
      timezone_long_name: (this.profile && this.profile.timezone_long_name) || (TIMEZONE ? timezoneLongName(TIMEZONE) : null),
      locale: (this.profile && this.profile.locale) || LOCALE || null,
      last_error: this.lastError || null,
    };
  }
}

const solver = new SolverPage();

function requireKey(req) {
  if (!SOLVER_KEY) return;
  const supplied = String(req.headers.authorization || '').replace(/^Bearer\s+/i, '').trim();
  const got = Buffer.from(supplied);
  const want = Buffer.from(SOLVER_KEY);
  if (got.length !== want.length || !crypto.timingSafeEqual(got, want)) {
    const err = new Error('invalid solver key');
    err.statusCode = 401;
    throw err;
  }
}

function readBody(req) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    let size = 0;
    req.on('data', (chunk) => {
      size += chunk.length;
      if (size > 8 * 1024 * 1024) {
        reject(new Error('body too large'));
        req.destroy();
        return;
      }
      chunks.push(chunk);
    });
    req.on('end', () => {
      try {
        resolve(chunks.length ? JSON.parse(Buffer.concat(chunks).toString('utf8')) : {});
      } catch (err) {
        reject(new Error('invalid JSON body'));
      }
    });
    req.on('error', reject);
  });
}

function send(res, status, payload) {
  const body = JSON.stringify(payload);
  res.writeHead(status, {
    'Content-Type': 'application/json; charset=utf-8',
    'Content-Length': Buffer.byteLength(body),
  });
  res.end(body);
}

/** 挑战程序出错时会把错误信息 base64 成 “token”，这里识别出来并当作失败返回。 */
function tokenFailure(value) {
  const text = String(value == null ? '' : value);
  if (!text) return 'empty token';
  try {
    const decoded = Buffer.from(text, 'base64').toString('utf8');
    if (/^\d+:\s/.test(decoded) || /Error/.test(decoded)) return decoded;
  } catch (err) { /* ignore */ }
  return '';
}

const server = http.createServer(async (req, res) => {
  const url = new URL(req.url, 'http://localhost');
  try {
    if (req.method === 'GET' && url.pathname === '/health') {
      send(res, 200, solver.status());
      return;
    }
    if (req.method === 'GET' && url.pathname === '/debug/state') {
      requireKey(req);
      send(res, 200, await solver.debugState());
      return;
    }
    if (req.method === 'POST' && url.pathname === '/v1/turnstile') {
      requireKey(req);
      const body = await readBody(req);
      const dx = String(body.dx || '');
      const p = String(body.p || '');
      // requirements 是 prepare 的完整响应，纯挑战参数：有了它就调用页面自带的 turnstile 模块。
      const requirements = body.requirements && typeof body.requirements === 'object' ? body.requirements : null;
      if (!dx && !requirements) {
        send(res, 400, { detail: 'dx or requirements is required' });
        return;
      }
      const outcome = await solver.solveTurnstile(requirements, dx, p);
      const value = outcome.token == null ? '' : String(outcome.token);
      let failure = outcome.ok ? tokenFailure(value) : value;
      if (!failure && !value) failure = 'solve produced an empty token ' + JSON.stringify(solver.lastSolve || {});
      log(`turnstile dx_len=${dx.length} p_len=${p.length} via_sdk=${requirements ? 'yes' : 'no'} token_len=${value.length}${failure ? ` FAILED=${String(failure).slice(0, 200)}` : ''}`);
      if (failure) {
        send(res, 502, { detail: `turnstile solve failed: ${String(failure).slice(0, 300)}` });
        return;
      }
      send(res, 200, { token: value });
      return;
    }
    if (req.method === 'POST' && url.pathname === '/v1/so') {
      requireKey(req);
      const body = await readBody(req);
      const collector = String(body.collector_dx || '');
      const snapshot = String(body.snapshot_dx || '');
      const p = String(body.p || '');
      if (!collector || !snapshot) {
        send(res, 400, { detail: 'collector_dx and snapshot_dx are required' });
        return;
      }
      const token = await solver.evaluate(
        ([c, s, key]) => window.__sentinelVM.so(c, s, key),
        [collector, snapshot, p]
      );
      const value = token == null ? '' : String(token);
      const failure = tokenFailure(value);
      log(`so collector_len=${collector.length} snapshot_len=${snapshot.length} token_len=${value.length}${failure ? ` FAILED=${failure.slice(0, 200)}` : ''}`);
      if (failure) {
        send(res, 502, { detail: `so solve failed: ${failure.slice(0, 300)}` });
        return;
      }
      send(res, 200, { token: value });
      return;
    }
    send(res, 404, { detail: 'not found' });
  } catch (err) {
    const status = (err && err.statusCode) || 503;
    const detail = status === 401 ? errText(err) : `${url.pathname} failed: ${errText(err)}`;
    log('request failed', url.pathname, detail);
    send(res, status, { detail });
  }
});

let shuttingDown = false;
async function shutdown(signal) {
  if (shuttingDown) return;
  shuttingDown = true;
  log(`received ${signal}, shutting down`);
  server.close();
  await solver.stop();
  process.exit(0);
}
process.on('SIGTERM', () => { shutdown('SIGTERM'); });
process.on('SIGINT', () => { shutdown('SIGINT'); });

(async () => {
  try {
    await startLocalProxy();
    await solver.start();
  } catch (err) {
    solver.lastError = errText(err);
    log('solver start failed:', solver.lastError);
  }
  server.listen(PORT, '0.0.0.0', () => log(`listening on :${PORT}`));
})();
