const { defineConfig, devices } = require("@playwright/test");

// The test server is always loopback-only. Proxying its readiness probe can
// produce a false positive before the Go process has started.
for (const name of ["HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"]) {
  delete process.env[name];
}

module.exports = defineConfig({
  testDir: "./tests/web",
  globalSetup: require.resolve("./tests/web/global-setup"),
  outputDir: "test-results",
  reporter: process.env.CI ? [["line"], ["html", { open: "never" }]] : "list",
  use: {
    baseURL: "http://127.0.0.1:18080",
    locale: "zh-CN",
    launchOptions: { args: ["--no-proxy-server"] },
    trace: "retain-on-failure"
  },
  projects: [
    { name: "desktop", use: { ...devices["Desktop Chrome"] }, grep: /@desktop/ },
    { name: "mobile", use: { ...devices["Pixel 7"] }, grep: /@mobile/ }
  ]
});
