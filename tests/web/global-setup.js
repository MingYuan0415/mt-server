const { execFileSync, spawn } = require("node:child_process");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");

module.exports = async function globalSetup() {
  const root = path.resolve(__dirname, "../..");
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "mt-server-playwright-"));
  const binary = path.join(directory, "mt-server");
  execFileSync("go", ["build", "-trimpath", "-o", binary, "./cmd/mt-server"], {
    cwd: root,
    stdio: "inherit"
  });

  const child = spawn(binary, [], {
    cwd: root,
    env: {
      ...process.env,
      MT_LISTEN_ADDR: "127.0.0.1:18080",
      MT_LOG_LEVEL: "info",
      MT_STATE_DIR: path.join(directory, "state"),
      MT_ADMIN_ALLOW_INSECURE_HTTP: "true"
    },
    stdio: ["ignore", "pipe", "pipe"]
  });

  try {
    await waitForReady(child);
  } catch (error) {
    child.kill("SIGKILL");
    fs.rmSync(directory, { recursive: true, force: true });
    throw error;
  }

  return async () => {
    child.kill("SIGTERM");
    await waitForExit(child);
    fs.rmSync(directory, { recursive: true, force: true });
  };
};

function waitForReady(child) {
  return new Promise((resolve, reject) => {
    let output = "";
    const timeout = setTimeout(() => reject(new Error("mt-server test process did not become ready")), 30000);
    const inspect = chunk => {
      output += chunk.toString();
      if (output.includes('"msg":"server listening"')) {
        clearTimeout(timeout);
        resolve();
      }
    };
    child.stdout.on("data", inspect);
    child.stderr.on("data", inspect);
    child.once("exit", code => {
      clearTimeout(timeout);
      reject(new Error(`mt-server test process exited with status ${code}: ${output}`));
    });
    child.once("error", error => {
      clearTimeout(timeout);
      reject(error);
    });
  });
}

function waitForExit(child) {
  if (child.exitCode !== null) return Promise.resolve();
  return new Promise(resolve => {
    const timeout = setTimeout(() => {
      child.kill("SIGKILL");
      resolve();
    }, 5000);
    child.once("exit", () => {
      clearTimeout(timeout);
      resolve();
    });
  });
}
