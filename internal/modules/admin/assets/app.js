const appState = { csrf: "", publicCSRF: "", status: null, setupOrigins: [] };
const views = ["setup-view", "login-view", "dashboard-view"];
const errorMessages = {
  qweather_credentials_rejected: "QWeather 凭据被拒绝，请核对 Project ID、Credential ID、API Host 和私钥是否属于同一项目。",
  qweather_rate_limited: "QWeather 请求频率受限，请稍后重试。",
  qweather_unavailable: "QWeather 服务暂时不可用，请稍后重试。",
  qweather_timeout: "QWeather 连接测试超时，请检查网络后重试。",
  test_location_unavailable: "请先取得浏览器临时位置，再进行 QWeather 验证。",
  invalid_test_location: "浏览器临时位置无效，请重新授权定位。",
  origin_rejected: "管理请求来源未被允许，请使用配置的公开 HTTPS 域名访问。",
  csrf_rejected: "管理会话已过期，请刷新页面后重新登录。",
  qweather_test_busy: "已有一个 QWeather 连接测试正在进行，请稍后重试。",
  qweather_test_rate_limited: "QWeather 连接测试次数已达上限，请稍后重试。",
  invalid_admin_origin: "请输入完整、有效的 HTTPS Origin。",
  admin_origin_exists: "该管理域名已存在。",
  admin_origin_limit_reached: "管理域名数量已达到上限。",
  admin_origin_not_found: "该管理域名已不存在，请刷新页面。",
  current_admin_origin: "不能删除当前正在使用的管理域名。",
  last_admin_origin: "HTTPS 代理模式必须保留至少一个管理域名。"
};

function showView(id) {
  for (const view of views) document.getElementById(view).classList.toggle("hidden", view !== id);
  document.getElementById("logout-button").classList.toggle("hidden", id !== "dashboard-view");
}

function showMessage(id, text, kind = "") {
  const node = document.getElementById(id);
  node.textContent = text;
  node.className = `message ${kind}`.trim();
  node.classList.toggle("hidden", !text);
}

async function api(path, options = {}) {
  const request = { credentials: "same-origin", ...options, headers: { ...(options.headers || {}) } };
  if (request.body && typeof request.body !== "string") {
    request.headers["Content-Type"] = "application/json";
    request.body = JSON.stringify(request.body);
  }
  if (request.method && !["GET", "HEAD"].includes(request.method)) {
    request.headers["X-CSRF-Token"] = appState.csrf || appState.publicCSRF;
  }
  const response = await fetch(`/admin/api/v1${path}`, request);
  if (response.headers.get("X-MT-State-Warning") === "durability_unconfirmed") {
    showDurabilityWarning();
  }
  let body = null;
  if (response.status !== 204) {
    try { body = await response.json(); } catch { body = null; }
  }
  if (!response.ok) {
    const code = body?.error?.code;
    const error = new Error(errorMessages[code] || body?.error?.message || `请求失败 (${response.status})`);
    error.status = response.status;
    error.code = code;
    throw error;
  }
  return body;
}

function setBusy(form, busy) {
  for (const control of form.querySelectorAll("button, input, textarea, select")) control.disabled = busy;
}

async function privateKeyFromForm(form, required) {
  const pasted = form.elements.private_key_pem?.value.trim() || "";
  const file = form.elements.private_key_file?.files?.[0];
  const value = file ? (await file.text()).trim() : pasted;
  if (required && !value) throw new Error("请选择或粘贴 Ed25519 私钥");
  return value;
}

function temporaryTestLocation(form) {
  const latitude = form.elements.test_latitude?.value || "";
  const longitude = form.elements.test_longitude?.value || "";
  if (!latitude || !longitude) return null;
  return {
    latitude: Number(latitude),
    longitude: Number(longitude),
    timezone: form.elements.test_timezone?.value || ""
  };
}

function clearTemporaryTestLocation(form) {
  for (const name of ["test_latitude", "test_longitude", "test_timezone"]) {
    if (form.elements[name]) form.elements[name].value = "";
  }
}

function useBrowserLocation(form, messageId) {
  if (!window.isSecureContext) {
    showMessage(messageId, "当前是非安全 HTTP 页面，浏览器不会提供定位授权，请手工填写本次测试坐标。", "error");
    return;
  }
  if (!navigator.geolocation) {
    showMessage(messageId, "当前浏览器不支持位置授权", "error");
    return;
  }
  navigator.geolocation.getCurrentPosition(position => {
    form.elements.test_latitude.value = position.coords.latitude.toFixed(6);
    form.elements.test_longitude.value = position.coords.longitude.toFixed(6);
    const timezone = form.elements.test_timezone;
    if (timezone && !timezone.value) timezone.value = Intl.DateTimeFormat().resolvedOptions().timeZone || "";
    showMessage(messageId, "已取得浏览器临时位置，本次验证后不会保存", "success");
  }, () => showMessage(messageId, "无法取得浏览器位置，请检查浏览器权限", "error"), {
    enableHighAccuracy: false, timeout: 8000, maximumAge: 300000
  });
}

function showVerification(id, value) {
  const container = document.getElementById(id);
  if (!value) {
    container.replaceChildren();
    container.classList.add("hidden");
    return;
  }
  const location = [value.location?.city, value.location?.region, value.location?.country]
    .filter(Boolean).join(" / ") ||
    (value.location?.source === "browser" ? "浏览器临时位置" : "设备提供位置");
  const items = [
    ["验证位置", location],
    ["当前天气", value.data?.condition_text || "-"],
    ["温度", `${value.data?.temperature_c ?? "-"} °C`],
    ["体感温度", `${value.data?.feels_like_c ?? "-"} °C`],
    ["湿度", `${value.data?.humidity_percent ?? "-"}%`],
    ["风速", `${value.data?.wind_speed_kmh ?? "-"} km/h`]
  ];
  const heading = document.createElement("strong");
  heading.textContent = "QWeather 实时验证结果";
  const list = document.createElement("dl");
  for (const [label, text] of items) {
    const row = document.createElement("div");
    const term = document.createElement("dt");
    const detail = document.createElement("dd");
    term.textContent = label;
    detail.textContent = text;
    row.append(term, detail);
    list.append(row);
  }
  const timestamp = document.createElement("small");
  const locationNotice = value.location?.source === "browser" ? " · 本次位置不会保存" : "";
  timestamp.textContent = `验证时间 ${new Date(value.tested_at).toLocaleString()} · 数据来源：和风天气 / QWeather${locationNotice}`;
  container.replaceChildren(heading, list, timestamp);
  container.classList.remove("hidden");
}

function showRawToken(value) {
  document.getElementById("raw-token").textContent = value;
  document.getElementById("token-dialog").showModal();
}

function showDurabilityWarning() {
  const node = document.getElementById("durability-warning");
  if (!node) return;
  node.textContent = "状态文件已更新，但系统尚未确认目录持久性。请检查状态卷并在确认后刷新页面。";
  node.className = "message warning";
}

function updateDurability(status) {
  const node = document.getElementById("durability-warning");
  if (!node) return;
  if (status.state_durability === "unconfirmed") showDurabilityWarning();
  else if (status.state_durability === "confirmed") {
    node.textContent = "";
    node.className = "message warning hidden";
  }
}

function validatePassword(value) {
  const codePoints = [...value].length;
  const bytes = new TextEncoder().encode(value).length;
  if (codePoints < 12 || bytes > 128) {
    return "密码必须至少包含 12 个 Unicode 字符，且 UTF-8 编码不超过 128 字节";
  }
  return "";
}

function normalizeAdminOrigin(value) {
  let parsed;
  try { parsed = new URL(value.trim()); } catch { throw new Error("请输入完整、有效的 HTTPS Origin"); }
  if (parsed.protocol !== "https:" || parsed.username || parsed.password ||
      parsed.pathname !== "/" || parsed.search || parsed.hash) {
    throw new Error("请输入完整、有效的 HTTPS Origin");
  }
  return parsed.origin;
}

function renderSetupOrigins() {
  const list = document.getElementById("setup-origin-list");
  list.replaceChildren();
  const current = window.location.protocol === "https:" ? window.location.origin : "";
  for (const origin of appState.setupOrigins) {
    const item = document.createElement("div");
    item.className = "origin-item";
    const value = document.createElement("code");
    value.textContent = origin;
    const button = document.createElement("button");
    button.type = "button";
    button.className = "danger";
    button.textContent = "删除";
    const currentProxyOrigin = appState.status?.admin_origin_mode === "proxy_allowlist" && origin === current;
    button.disabled = currentProxyOrigin;
    if (currentProxyOrigin) button.title = "当前管理域名不能删除";
    button.addEventListener("click", () => {
      appState.setupOrigins = appState.setupOrigins.filter(itemOrigin => itemOrigin !== origin);
      renderSetupOrigins();
    });
    item.append(value, button);
    list.append(item);
  }
}

function addSetupOrigin() {
  showMessage("setup-origin-message", "");
  const input = document.getElementById("setup-origin-input");
  try {
    const origin = normalizeAdminOrigin(input.value);
    if (appState.setupOrigins.includes(origin)) throw new Error("该管理域名已存在");
    if (appState.setupOrigins.length >= 16) throw new Error("管理域名数量已达到上限");
    appState.setupOrigins.push(origin);
    input.value = "";
    renderSetupOrigins();
  } catch (error) { showMessage("setup-origin-message", error.message, "error"); }
}

function configureSetupOrigins(status) {
  const proxyMode = status.admin_origin_mode === "proxy_allowlist";
  document.getElementById("setup-origin-mode").textContent = proxyMode
    ? "当前 HTTPS 域名会作为管理入口保存。"
    : "可预先保存未来 HTTPS 代理使用的管理域名。";
  if (!status.configured && proxyMode && window.location.protocol === "https:" &&
      !appState.setupOrigins.includes(window.location.origin)) {
    appState.setupOrigins.unshift(window.location.origin);
  }
  renderSetupOrigins();
}

async function refreshStatus() {
  const status = await api("/status");
  appState.status = status;
  appState.publicCSRF = status.csrf_token;
  configureSetupOrigins(status);
  const statusNode = document.getElementById("service-status");
  statusNode.textContent = status.status === "ready" ? "服务正常" : status.status === "setup_required" ? "等待初始化" : "服务不可用";
  document.getElementById("overview-ready").textContent = statusNode.textContent;
  document.getElementById("overview-version").textContent = status.version;
  document.getElementById("overview-transport").textContent = status.secure_transport ? "HTTPS" : "HTTP";
  updateDurability(status);
  return status;
}

async function loadDashboard() {
  const session = await api("/session");
  appState.csrf = session.csrf_token;
  showView("dashboard-view");
  await Promise.all([loadQWeather(), loadAdminOrigins(), loadTokens(), refreshStatus()]);
}

async function loadQWeather() {
  const value = await api("/settings/qweather");
  const form = document.getElementById("qweather-form");
  form.elements.api_host.value = value.api_host;
  form.elements.project_id.value = value.project_id;
  form.elements.credential_id.value = value.credential_id;
  form.elements.private_key_pem.value = "";
  form.elements.private_key_file.value = "";
  document.getElementById("key-state").textContent = value.private_key_configured ? "已配置" : "未配置";
  document.getElementById("key-fingerprint").textContent = value.public_key_fingerprint || "-";
}

async function loadAdminOrigins() {
  const value = await api("/settings/admin-origins");
  const list = document.getElementById("origin-list");
  list.replaceChildren();
  document.getElementById("origin-mode").textContent = value.mode === "proxy_allowlist"
    ? "列表中的 HTTPS Origin 当前用于管理同源校验。"
    : "当前使用直接同源校验；这些域名会在切换到 HTTPS 代理模式后生效。";
  const current = window.location.protocol === "https:" ? window.location.origin : "";
  for (const entry of value.origins) {
    const item = document.createElement("div");
    item.className = "origin-item";
    const description = document.createElement("div");
    const origin = document.createElement("code");
    origin.textContent = entry.origin;
    description.append(origin);
    if (entry.origin === current) {
      const marker = document.createElement("small");
      marker.textContent = "当前入口";
      description.append(marker);
    }
    const button = document.createElement("button");
    button.type = "button";
    button.className = "danger";
    button.textContent = "删除";
    button.disabled = entry.origin === current;
    if (button.disabled) button.title = "当前管理域名不能删除";
    button.addEventListener("click", async () => {
      if (!confirm(`删除管理域名“${entry.origin}”？`)) return;
      showMessage("origin-message", "");
      try {
        await api(`/settings/admin-origins/${encodeURIComponent(entry.id)}`, { method: "DELETE" });
        appState.csrf = "";
        showView("login-view");
        showMessage("login-message", "管理域名已删除，请重新登录", "success");
      } catch (error) { showMessage("origin-message", error.message, "error"); }
    });
    item.append(description, button);
    list.append(item);
  }
}

async function loadTokens() {
  const value = await api("/device-tokens");
  const list = document.getElementById("token-list");
  list.replaceChildren();
  for (const token of value.tokens) {
    const item = document.createElement("div");
    item.className = "token-item";
    const description = document.createElement("div");
    const name = document.createElement("strong");
    name.textContent = token.name;
    const details = document.createElement("small");
    details.textContent = `${token.id} · ${new Date(token.created_at).toLocaleString()}`;
    description.append(name, details);
    const button = document.createElement("button");
    button.type = "button";
    button.className = "danger";
    button.textContent = "撤销";
    button.addEventListener("click", async () => {
      if (!confirm(`撤销设备“${token.name}”的令牌？`)) return;
      try {
        await api(`/device-tokens/${encodeURIComponent(token.id)}`, { method: "DELETE" });
        try { await Promise.all([loadTokens(), refreshStatus()]); }
        catch { showMessage("token-message", "令牌已撤销，但页面刷新失败，请手动刷新。", "warning"); }
      }
      catch (error) { showMessage("token-message", error.message, "error"); }
    });
    item.append(description, button);
    list.append(item);
  }
}

document.getElementById("setup-form").addEventListener("submit", async event => {
  event.preventDefault();
  const form = event.currentTarget;
  showMessage("setup-message", "");
  if (form.elements.password.value !== form.elements.password_confirm.value) {
    showMessage("setup-message", "两次输入的管理员密码不一致", "error"); return;
  }
  const passwordError = validatePassword(form.elements.password.value);
  if (passwordError) { showMessage("setup-message", passwordError, "error"); return; }
  if (appState.status?.admin_origin_mode === "proxy_allowlist" &&
      !appState.setupOrigins.includes(window.location.origin)) {
    showMessage("setup-message", "管理域名列表必须包含当前 HTTPS Origin", "error"); return;
  }
  setBusy(form, true);
  try {
    const privateKey = await privateKeyFromForm(form, true);
    const response = await api("/setup", {
      method: "POST",
      body: {
        password: form.elements.password.value,
        qweather: {
          api_host: form.elements.api_host.value.trim(),
          project_id: form.elements.project_id.value.trim(),
          credential_id: form.elements.credential_id.value.trim(),
          private_key_pem: privateKey,
          test_location: temporaryTestLocation(form)
        },
        device_name: form.elements.device_name.value.trim(),
        admin_origins: [...appState.setupOrigins]
      }
    });
    appState.csrf = response.csrf_token;
    showVerification("overview-verification", response.verification);
    showRawToken(response.device_token);
    form.reset();
    try { await loadDashboard(); }
    catch (error) { showMessage("setup-message", `初始化已完成，但仪表盘刷新失败：${error.message}`, "error"); }
  } catch (error) { showMessage("setup-message", error.message, "error"); }
  finally { setBusy(form, false); renderSetupOrigins(); }
});

document.getElementById("login-form").addEventListener("submit", async event => {
  event.preventDefault(); const form = event.currentTarget; setBusy(form, true); showMessage("login-message", "");
  try { const value = await api("/session", { method: "POST", body: { password: form.elements.password.value } }); appState.csrf = value.csrf_token; form.reset(); await loadDashboard(); }
  catch (error) { showMessage("login-message", error.message, "error"); }
  finally { setBusy(form, false); }
});

document.getElementById("logout-button").addEventListener("click", async () => {
  try { await api("/session", { method: "DELETE" }); } catch {}
  appState.csrf = ""; showView("login-view");
});

for (const tab of document.querySelectorAll(".tab")) tab.addEventListener("click", () => {
  for (const item of document.querySelectorAll(".tab")) item.classList.toggle("active", item === tab);
  for (const panel of document.querySelectorAll(".panel")) panel.classList.toggle("active", panel.dataset.panel === tab.dataset.tab);
});

document.getElementById("setup-test-browser-location").addEventListener("click", () => useBrowserLocation(document.getElementById("setup-form"), "setup-message"));
document.getElementById("setup-origin-add").addEventListener("click", addSetupOrigin);
document.getElementById("setup-origin-input").addEventListener("keydown", event => {
  if (event.key === "Enter") { event.preventDefault(); addSetupOrigin(); }
});
document.getElementById("qweather-test-browser-location").addEventListener("click", () => useBrowserLocation(document.getElementById("qweather-form"), "qweather-message"));

async function qweatherPayload(form) {
  return {
    api_host: form.elements.api_host.value.trim(),
    project_id: form.elements.project_id.value.trim(),
    credential_id: form.elements.credential_id.value.trim(),
    private_key_pem: await privateKeyFromForm(form, false),
    test_location: temporaryTestLocation(form)
  };
}

document.getElementById("qweather-test").addEventListener("click", async () => {
  const form = document.getElementById("qweather-form"); setBusy(form, true); showMessage("qweather-message", "");
  try { const value = await api("/settings/qweather/test", { method: "POST", body: await qweatherPayload(form) }); showMessage("qweather-message", "连接测试成功", "success"); showVerification("qweather-verification", value.verification); clearTemporaryTestLocation(form); }
  catch (error) { showMessage("qweather-message", error.message, "error"); }
  finally { setBusy(form, false); }
});

document.getElementById("qweather-form").addEventListener("submit", async event => {
  event.preventDefault(); const form = event.currentTarget; setBusy(form, true); showMessage("qweather-message", "");
  try {
    const value = await api("/settings/qweather", { method: "PUT", body: await qweatherPayload(form) });
    showMessage("qweather-message", "QWeather 配置已保存并生效", "success");
    showVerification("qweather-verification", value.verification); clearTemporaryTestLocation(form);
    try { await Promise.all([loadQWeather(), refreshStatus()]); }
    catch { showMessage("qweather-message", "QWeather 配置已保存，但页面刷新失败，请手动刷新。", "warning"); }
  }
  catch (error) { showMessage("qweather-message", error.message, "error"); }
  finally { setBusy(form, false); }
});

document.getElementById("token-form").addEventListener("submit", async event => {
  event.preventDefault(); const form = event.currentTarget; setBusy(form, true); showMessage("token-message", "");
  try {
    const value = await api("/device-tokens", { method: "POST", body: { name: form.elements.name.value.trim() } });
    form.reset(); showRawToken(value.device_token);
    try { await Promise.all([loadTokens(), refreshStatus()]); }
    catch { showMessage("token-message", "令牌已创建并显示，但页面刷新失败，请手动刷新。", "warning"); }
  }
  catch (error) { showMessage("token-message", error.message, "error"); }
  finally { setBusy(form, false); }
});

document.getElementById("origin-form").addEventListener("submit", async event => {
  event.preventDefault();
  const form = event.currentTarget;
  setBusy(form, true);
  showMessage("origin-message", "");
  try {
    const origin = normalizeAdminOrigin(form.elements.origin.value);
    await api("/settings/admin-origins", { method: "POST", body: { origin } });
    form.reset();
    showMessage("origin-message", "管理域名已添加并生效", "success");
    try { await loadAdminOrigins(); }
    catch { showMessage("origin-message", "管理域名已添加，但列表刷新失败，请手动刷新。", "warning"); }
  } catch (error) { showMessage("origin-message", error.message, "error"); }
  finally { setBusy(form, false); }
});

document.getElementById("password-form").addEventListener("submit", async event => {
  event.preventDefault(); const form = event.currentTarget; showMessage("password-message", "");
  if (form.elements.new_password.value !== form.elements.new_password_confirm.value) { showMessage("password-message", "两次输入的新密码不一致", "error"); return; }
  const passwordError = validatePassword(form.elements.new_password.value);
  if (passwordError) { showMessage("password-message", passwordError, "error"); return; }
  setBusy(form, true);
  try { await api("/account/password", { method: "PUT", body: { current_password: form.elements.current_password.value, new_password: form.elements.new_password.value } }); appState.csrf = ""; form.reset(); showView("login-view"); showMessage("login-message", "密码已更新，请重新登录", "success"); }
  catch (error) { showMessage("password-message", error.message, "error"); }
  finally { setBusy(form, false); }
});

document.getElementById("copy-token").addEventListener("click", async () => {
  try { await navigator.clipboard.writeText(document.getElementById("raw-token").textContent); document.getElementById("copy-token").textContent = "已复制"; } catch { document.getElementById("copy-token").textContent = "复制失败"; }
});
document.getElementById("close-token").addEventListener("click", () => {
  document.getElementById("token-dialog").close();
});
document.getElementById("token-dialog").addEventListener("close", () => {
  document.getElementById("raw-token").textContent = "";
  document.getElementById("copy-token").textContent = "复制";
});
document.getElementById("token-dialog").addEventListener("cancel", event => event.preventDefault());

(async () => {
  try {
    const status = await refreshStatus();
    if (!status.configured) { showView("setup-view"); return; }
    try { await loadDashboard(); } catch (error) { if (error.status === 401) showView("login-view"); else throw error; }
  } catch {
    document.getElementById("service-status").textContent = "连接失败";
    showView("login-view");
    showMessage("login-message", "无法连接管理服务", "error");
  }
})();
