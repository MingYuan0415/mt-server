const { test, expect } = require("@playwright/test");

const verification = {
  source: { id: "qweather", name: "QWeather", attribution_url: "https://www.qweather.com/" },
  location: { timezone: "Asia/Shanghai", source: "browser", provider: "browser", precision: "coarse" },
  tested_at: "2026-08-02T01:00:00Z",
  updated_at: "2026-08-02T00:55:00Z",
  data: {
    observed_at: "2026-08-02T00:50:00Z", temperature_c: 28, feels_like_c: 31,
    condition_code: "101", condition_text: "多云", wind_degrees: 135,
    wind_direction: "东南风", wind_scale: "2", wind_speed_kmh: 8,
    humidity_percent: 72, precipitation_mm: 0, pressure_hpa: 1004, visibility_km: 16
  }
};

const qweather = {
  api_host: "account-id.re.qweatherapi.com",
  project_id: "project-id",
  credential_id: "credential-id",
  private_key_configured: true,
  public_key_fingerprint: "example-public-key-fingerprint",
  language: "zh",
  unit: "m"
};

async function installBackend(page, initialConfigured) {
  const backend = {
    configured: initialConfigured,
    loggedIn: false,
    publicCSRF: "public-csrf-token",
    sessionCSRF: "session-csrf-token",
    qweather: { ...qweather },
    origins: [],
    tokens: [{ id: "device_initial", name: "初始设备", created_at: "2026-08-02T00:00:00Z" }],
    stateDurability: initialConfigured ? "confirmed" : "not_applicable",
    warnNextWrite: false,
    failNextSessionLoad: false,
    failDiagnostics: false,
    diagnosticsRequests: 0,
    writes: []
  };

  await page.route("**/admin/api/v1/**", async route => {
    const request = route.request();
    const url = new URL(request.url());
    const path = url.pathname.replace("/admin/api/v1", "");
    const method = request.method();
    const write = !["GET", "HEAD"].includes(method);
    const json = (status, body, headers = {}) => {
      if (write && backend.warnNextWrite) {
        headers["X-MT-State-Warning"] = "durability_unconfirmed";
        backend.warnNextWrite = false;
        backend.stateDurability = "unconfirmed";
      }
      return route.fulfill({
        status,
        contentType: "application/json",
        headers: { "Cache-Control": "private, no-store", ...headers },
        body: body === undefined ? "" : JSON.stringify(body)
      });
    };

    if (path === "/status" && method === "GET") {
      return json(200, {
        configured: backend.configured,
        status: backend.configured ? "ready" : "setup_required",
        version: "web-test",
        secure_transport: false,
        admin_origin_mode: "direct_same_origin",
        state_durability: backend.stateDurability,
        csrf_token: backend.publicCSRF
      });
    }

    if (write) {
      backend.writes.push({
        path,
        csrf: request.headers()["x-csrf-token"] || "",
        authorization: request.headers().authorization || "",
        body: request.postDataJSON?.() || null
      });
    }

    if (path === "/setup" && method === "POST") {
      backend.configured = true;
      backend.loggedIn = true;
      backend.stateDurability = "confirmed";
      return json(201, {
        csrf_token: backend.sessionCSRF,
        device_token: "mt_first-device-token",
        device: backend.tokens[0],
        qweather_public_key_fingerprint: qweather.public_key_fingerprint,
        verification
      }, { "Set-Cookie": "mt_admin_session=browser-test; Path=/admin/; HttpOnly; SameSite=Strict" });
    }
    if (path === "/session" && method === "GET") {
      if (backend.failNextSessionLoad) {
        backend.failNextSessionLoad = false;
        return json(500, { error: { code: "state_unavailable", message: "temporary failure" } });
      }
      return backend.loggedIn ? json(200, { csrf_token: backend.sessionCSRF }) :
        json(401, { error: { code: "admin_unauthorized", message: "需要登录" } });
    }
    if (path === "/session" && method === "POST") {
      backend.loggedIn = true;
      return json(200, { csrf_token: backend.sessionCSRF }, {
        "Set-Cookie": "mt_admin_session=browser-test; Path=/admin/; HttpOnly; SameSite=Strict"
      });
    }
    if (path === "/session" && method === "DELETE") {
      backend.loggedIn = false;
      return json(200, { status: "logged_out" });
    }
    if (path === "/diagnostics" && method === "GET") {
      backend.diagnosticsRequests++;
      if (backend.failDiagnostics) {
        return json(503, { error: { code: "diagnostics_unavailable", message: "unavailable" } });
      }
      return json(200, {
        generated_at: "2026-08-02T02:00:00Z",
        runtime_started_at: "2026-08-02T01:00:00Z",
        provider: { status: "ready" },
        last_success_at: "2026-08-02T01:55:00Z",
        locations: 1,
        entries: 4,
        kinds: {
          current: { entries: 1, requests: 3, fresh_hits: 2, stale_hits: 0, fetch_successes: 1, fetch_failures: 0 },
          hourly: { entries: 1, requests: 1, fresh_hits: 0, stale_hits: 0, fetch_successes: 1, fetch_failures: 0 },
          daily: { entries: 1, requests: 1, fresh_hits: 0, stale_hits: 0, fetch_successes: 1, fetch_failures: 0 },
          alerts: { entries: 1, requests: 1, fresh_hits: 0, stale_hits: 0, fetch_successes: 1, fetch_failures: 0 }
        }
      });
    }
    if (path === "/settings/qweather" && method === "GET") return json(200, backend.qweather);
    if (path === "/settings/qweather/test" && method === "POST") {
      return json(200, { status: "ok", public_key_fingerprint: qweather.public_key_fingerprint, verification });
    }
    if (path === "/settings/qweather" && method === "PUT") {
      backend.qweather = { ...backend.qweather, ...request.postDataJSON(), private_key_configured: true };
      return json(200, { status: "saved", public_key_fingerprint: qweather.public_key_fingerprint, verification });
    }
    if (path === "/settings/admin-origins" && method === "GET") {
      return json(200, { mode: "direct_same_origin", maximum: 16, origins: backend.origins });
    }
    if (path === "/settings/admin-origins" && method === "POST") {
      const origin = request.postDataJSON().origin;
      const entry = { id: `origin-${backend.origins.length + 1}`, origin };
      backend.origins.push(entry);
      return json(201, entry);
    }
    if (path.startsWith("/settings/admin-origins/") && method === "DELETE") {
      const id = decodeURIComponent(path.slice("/settings/admin-origins/".length));
      backend.origins = backend.origins.filter(origin => origin.id !== id);
      backend.loggedIn = false;
      return json(204);
    }
    if (path === "/device-tokens" && method === "GET") return json(200, { tokens: backend.tokens });
    if (path === "/device-tokens" && method === "POST") {
      const token = { id: "device_second", name: request.postDataJSON().name, created_at: "2026-08-02T01:00:00Z" };
      backend.tokens.push(token);
      return json(201, { device_token: "mt_second-device-token", device: token });
    }
    if (path.startsWith("/device-tokens/") && method === "DELETE") {
      const id = decodeURIComponent(path.slice("/device-tokens/".length));
      backend.tokens = backend.tokens.filter(token => token.id !== id);
      return json(204);
    }
    return json(404, { error: { code: "not_found", message: "not found" } });
  });
  return backend;
}

async function expectStableLayout(page) {
  const result = await page.evaluate(() => {
    const visible = element => {
      const style = getComputedStyle(element);
      const rect = element.getBoundingClientRect();
      return style.display !== "none" && style.visibility !== "hidden" && rect.width > 0 && rect.height > 0;
    };
    const controls = [...document.querySelectorAll("button, input, textarea, select")].filter(visible);
    const overlaps = [];
    for (let left = 0; left < controls.length; left++) {
      for (let right = left + 1; right < controls.length; right++) {
        const a = controls[left];
        const b = controls[right];
        if (a.contains(b) || b.contains(a)) continue;
        const ar = a.getBoundingClientRect();
        const br = b.getBoundingClientRect();
        if (ar.left < br.right && ar.right > br.left && ar.top < br.bottom && ar.bottom > br.top) {
          overlaps.push(`${a.tagName}:${a.textContent.trim()} / ${b.tagName}:${b.textContent.trim()}`);
        }
      }
    }
    return {
      viewportOverflow: document.documentElement.scrollWidth > window.innerWidth + 1,
      overlaps
    };
  });
  expect(result).toEqual({ viewportOverflow: false, overlaps: [] });
}

async function expectNoBrowserStorage(page) {
  const stored = await page.evaluate(async () => ({
    local: Object.keys(localStorage),
    session: Object.keys(sessionStorage),
    databases: indexedDB.databases ? (await indexedDB.databases()).map(item => item.name) : []
  }));
  expect(stored).toEqual({ local: [], session: [], databases: [] });
}

test("@desktop completes setup and rotates configuration", async ({ page }, testInfo) => {
  const backend = await installBackend(page, false);
  await page.context().grantPermissions(["geolocation"]);
  await page.context().setGeolocation({ latitude: 30.2, longitude: 120.1 });
  await page.goto("/admin/");
  await expect(page.getByRole("heading", { name: "连接和风天气" })).toBeVisible();

  const setup = page.locator("#setup-form");
  await setup.locator('[name="password"]').fill("correct horse battery staple");
  await setup.locator('[name="password_confirm"]').fill("correct horse battery staple");
  await setup.locator('[name="api_host"]').fill(qweather.api_host);
  await setup.locator('[name="project_id"]').fill(qweather.project_id);
  await setup.locator('[name="credential_id"]').fill(qweather.credential_id);
  await setup.locator('[name="private_key_pem"]').fill("test-ed25519-key-material");
  await setup.getByRole("button", { name: "使用浏览器临时位置" }).click();
  await expect(setup.locator('[name="test_latitude"]')).toHaveValue("30.200000");
  await expectStableLayout(page);
  await page.screenshot({ path: testInfo.outputPath("desktop-setup.png"), fullPage: true });
  await setup.getByRole("button", { name: "测试并完成初始化" }).click();

  await expect(page.locator("#token-dialog")).toBeVisible();
  await expect(page.locator("#raw-token")).toHaveText("mt_first-device-token");
  await expect(page.locator("#dashboard-view")).toBeVisible();
  expect(backend.writes[0]).toMatchObject({
    path: "/setup",
    csrf: backend.publicCSRF,
    authorization: "",
    body: expect.objectContaining({
      qweather: expect.objectContaining({
        test_location: expect.objectContaining({ latitude: 30.2, longitude: 120.1 })
      })
    })
  });
  expect(backend.writes[0].body).not.toHaveProperty("location");
  expect(backend.writes[0].body.admin_origins).toEqual([]);
  await page.getByRole("button", { name: "我已保存" }).click();
  await expect(page.locator("#raw-token")).toHaveText("");
  await expect(page.locator("#overview-verification")).toContainText("浏览器临时位置");
  await expect(page.locator("#overview-verification")).not.toContainText("Asia/Shanghai");
  await expect(page.locator("#overview-verification")).toContainText("28 °C");
  await expect(page.locator("#diagnostics-provider")).toHaveText("正常");
  await expect(page.locator("#diagnostics-kinds")).toContainText("天气预警");
  await expectStableLayout(page);
  await page.screenshot({ path: testInfo.outputPath("desktop-verification.png"), fullPage: true });

  await page.getByRole("button", { name: "和风天气" }).click();
  await page.locator('#qweather-form [name="project_id"]').fill("project-next");
  await page.locator("#qweather-test-browser-location").click();
  await page.getByRole("button", { name: "测试并保存" }).click();
  await expect(page.locator("#qweather-message")).toContainText("已保存并生效");
  await expect(page.locator("#qweather-verification")).toContainText("多云");

  await page.getByRole("button", { name: "管理域名" }).click();
  await page.locator('#origin-form [name="origin"]').fill("https://admin.example.com");
  await page.locator("#origin-form").getByRole("button", { name: "添加" }).click();
  await expect(page.locator("#origin-list")).toContainText("https://admin.example.com");

  await page.getByRole("button", { name: "设备令牌" }).click();
  backend.warnNextWrite = true;
  await page.locator('#token-form [name="name"]').fill("轮换设备");
  await page.getByRole("button", { name: "创建令牌" }).click();
  await expect(page.locator("#raw-token")).toHaveText("mt_second-device-token");
  await page.getByRole("button", { name: "我已保存" }).click();
  await expect(page.locator(".token-item")).toHaveCount(2);
  await expect(page.locator("#durability-warning")).toContainText("尚未确认目录持久性");

  await expectStableLayout(page);
  await expectNoBrowserStorage(page);
  await page.screenshot({ path: testInfo.outputPath("desktop-devices.png"), fullPage: true });
});

test("@mobile logs in and keeps the settings layout usable", async ({ page }, testInfo) => {
  const backend = await installBackend(page, true);
  backend.failDiagnostics = true;
  backend.origins = [{ id: "origin-current", origin: "https://admin.example.test:18443" }];
  await page.goto("/admin/");
  await expect(page.getByRole("heading", { name: "mt-server" })).toBeVisible();
  await page.locator('#login-form [name="password"]').fill("correct horse battery staple");
  await page.getByRole("button", { name: "登录" }).click();
  await expect(page.locator("#dashboard-view")).toBeVisible();
  await expect(page.locator("#diagnostics-message")).toContainText("运行诊断暂时不可用");
  expect(backend.writes[0]).toMatchObject({ path: "/session", csrf: backend.publicCSRF });

  await page.getByRole("button", { name: "管理域名" }).click();
  const currentOrigin = page.locator(".origin-item").filter({ hasText: "https://admin.example.test:18443" });
  await expect(currentOrigin.getByRole("button", { name: "删除" })).toBeDisabled();

  await page.getByRole("button", { name: "和风天气" }).click();
  await expect(page.getByRole("heading", { name: "QWeather" })).toBeVisible();
  await expect(page.locator('#qweather-form [name="test_latitude"]')).toBeVisible();
  await expectStableLayout(page);
  await expectNoBrowserStorage(page);
  await page.screenshot({ path: testInfo.outputPath("mobile-qweather.png"), fullPage: true });
});

test("@mobile renders and scrolls diagnostics without viewport overflow", async ({ page }, testInfo) => {
  const backend = await installBackend(page, true);
  await page.goto("/admin/");
  await page.locator('#login-form [name="password"]').fill("correct horse battery staple");
  await page.getByRole("button", { name: "登录" }).click();
  await expect(page.locator("#dashboard-view")).toBeVisible();
  await expect(page.locator("#diagnostics-provider")).toHaveText("正常");
  await expect(page.locator("#diagnostics-kinds")).toContainText("实时天气");
  await expect(page.locator("#diagnostics-kinds")).toContainText("天气预警");
  await expectStableLayout(page);

  const table = page.locator(".diagnostics-table-wrap");
  await expect.poll(async () => table.evaluate(node => node.scrollWidth > node.clientWidth)).toBe(true);
  const scrollable = await table.evaluate(node => {
    node.scrollLeft = 120;
    return { scrollWidth: node.scrollWidth, clientWidth: node.clientWidth, scrollLeft: node.scrollLeft };
  });
  expect(scrollable.scrollWidth).toBeGreaterThan(scrollable.clientWidth);
  expect(scrollable.scrollLeft).toBeGreaterThan(0);

  const beforeRefresh = backend.diagnosticsRequests;
  const refresh = page.locator("#refresh-diagnostics");
  await refresh.click();
  await expect(refresh).toBeEnabled();
  await expect(page.locator("#diagnostics-message")).toContainText("诊断生成于");
  await refresh.click();
  await expect(refresh).toBeEnabled();
  await expect(page.locator("#diagnostics-kinds")).toContainText("逐日");
  expect(backend.diagnosticsRequests).toBeGreaterThanOrEqual(beforeRefresh + 2);
  await expectStableLayout(page);
  await expectNoBrowserStorage(page);
  await page.screenshot({ path: testInfo.outputPath("mobile-diagnostics.png"), fullPage: true });
});

test("@desktop real management handler completes the lifecycle", async ({ page }) => {
  await page.goto("/admin/");
  await expect(page.getByRole("heading", { name: "连接和风天气" })).toBeVisible();
  await expect(page.locator("#setup-origin-list")).toContainText("https://admin.example.test:18443");

  const setup = page.locator("#setup-form");
  await setup.locator('[name="password"]').fill("correct horse battery staple");
  await setup.locator('[name="password_confirm"]').fill("correct horse battery staple");
  await setup.locator('[name="api_host"]').fill(qweather.api_host);
  await setup.locator('[name="project_id"]').fill(qweather.project_id);
  await setup.locator('[name="credential_id"]').fill(qweather.credential_id);
  await setup.locator('[name="private_key_pem"]').fill("test-private-key");
  await setup.locator('[name="test_latitude"]').fill("30.2");
  await setup.locator('[name="test_longitude"]').fill("120.1");
  await setup.getByRole("button", { name: "测试并完成初始化" }).click();

  await expect(page.locator("#token-dialog")).toBeVisible();
  await expect(page.locator("#raw-token")).toContainText("mt_");
  const firstToken = await page.locator("#raw-token").textContent();
  await page.keyboard.press("Escape");
  await expect(page.locator("#token-dialog")).toBeVisible();
  await expect(page.locator("#raw-token")).toHaveText(firstToken);
  await page.getByRole("button", { name: "我已保存" }).click();

  await page.getByRole("button", { name: "管理域名" }).click();
  await page.locator('#origin-form [name="origin"]').fill("https://new.example.test:18443");
  await page.locator("#origin-form").getByRole("button", { name: "添加" }).click();
  await expect(page.locator("#origin-list")).toContainText("https://new.example.test:18443");
  await page.goto("https://new.example.test:18443/admin/");
  await page.locator('#login-form [name="password"]').fill("correct horse battery staple");
  await page.getByRole("button", { name: "登录" }).click();
  await page.getByRole("button", { name: "管理域名" }).click();
  const oldOrigin = page.locator(".origin-item").filter({ hasText: "https://admin.example.test:18443" });
  page.once("dialog", dialog => dialog.accept());
  await oldOrigin.getByRole("button", { name: "删除" }).click();
  await expect(page.locator("#login-view")).toBeVisible();
  await page.locator('#login-form [name="password"]').fill("correct horse battery staple");
  await page.getByRole("button", { name: "登录" }).click();

  await page.getByRole("button", { name: "和风天气" }).click();
  await page.locator('#qweather-form [name="project_id"]').fill("project-next");
  await page.locator('#qweather-form [name="test_latitude"]').fill("30.2");
  await page.locator('#qweather-form [name="test_longitude"]').fill("120.1");
  await page.getByRole("button", { name: "测试并保存" }).click();
  await expect(page.locator("#qweather-message")).toContainText("已保存并生效");

  await page.getByRole("button", { name: "设备令牌" }).click();
  await page.locator('#token-form [name="name"]').fill("轮换设备");
  await page.getByRole("button", { name: "创建令牌" }).click();
  await expect(page.locator("#raw-token")).toContainText("mt_");
  await page.getByRole("button", { name: "我已保存" }).click();
  await expect(page.locator(".token-item")).toHaveCount(2);

  await page.getByRole("button", { name: "账户" }).click();
  await page.locator('#password-form [name="current_password"]').fill("correct horse battery staple");
  await page.locator('#password-form [name="new_password"]').fill("replacement password value");
  await page.locator('#password-form [name="new_password_confirm"]').fill("replacement password value");
  await page.getByRole("button", { name: "更新密码" }).click();
  await expect(page.locator("#login-view")).toBeVisible();
  await page.locator('#login-form [name="password"]').fill("replacement password value");
  await page.getByRole("button", { name: "登录" }).click();
  await expect(page.locator("#dashboard-view")).toBeVisible();
  await expectNoBrowserStorage(page);
});

test("@desktop setup token survives dashboard refresh failure", async ({ page }) => {
  const backend = await installBackend(page, false);
  await page.goto("/admin/");
  const setup = page.locator("#setup-form");
  await setup.locator('[name="password"]').fill("correct horse battery staple");
  await setup.locator('[name="password_confirm"]').fill("correct horse battery staple");
  await setup.locator('[name="api_host"]').fill(qweather.api_host);
  await setup.locator('[name="project_id"]').fill(qweather.project_id);
  await setup.locator('[name="credential_id"]').fill(qweather.credential_id);
  await setup.locator('[name="private_key_pem"]').fill("test-ed25519-key-material");
  await setup.locator('[name="test_latitude"]').fill("30.2");
  await setup.locator('[name="test_longitude"]').fill("120.1");
  backend.failNextSessionLoad = true;
  await setup.getByRole("button", { name: "测试并完成初始化" }).click();
  await expect(page.locator("#token-dialog")).toBeVisible();
  await expect(page.locator("#raw-token")).toHaveText("mt_first-device-token");
  await expect(page.locator("#setup-message")).toContainText("初始化已完成，但仪表盘刷新失败");
});

test("@desktop insecure non-loopback HTTP asks for manual test coordinates", async ({ page }) => {
  await installBackend(page, false);
  await page.goto("http://insecure.example.test:18080/admin/");
  const setup = page.locator("#setup-form");
  await setup.getByRole("button", { name: "使用浏览器临时位置" }).click();
  await expect(page.locator("#setup-message")).toContainText("非安全 HTTP 页面");
  await expect(setup.locator('[name="test_latitude"]')).toHaveValue("");
  await expect(setup.locator('[name="test_longitude"]')).toHaveValue("");
});
