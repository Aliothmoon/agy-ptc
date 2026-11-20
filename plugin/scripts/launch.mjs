#!/usr/bin/env node
import { spawn } from "node:child_process";
import { existsSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const binName = process.platform === "win32" ? "agy-ptc.exe" : "agy-ptc";

function resolveBinary() {
  const candidates = [
    path.join(__dirname, binName),
    path.resolve(__dirname, "..", "..", binName),
  ];
  for (const candidate of candidates) {
    if (existsSync(candidate)) {
      return candidate;
    }
  }
  return binName;
}

const binPath = resolveBinary();
const child = spawn(binPath, process.argv.slice(2), {
  stdio: "inherit",
  windowsHide: true,
});

child.on("error", (err) => {
  if (process.argv[2] === "hook") {
    process.stdout.write("{}\n");
    process.exit(0);
  }
  process.stderr.write(`agy-ptc launch error (${binPath}): ${err.message}\n`);
  process.exit(1);
});

child.on("exit", (code, signal) => {
  if (signal) {
    process.kill(process.pid, signal);
    return;
  }
  process.exit(code ?? 0);
});
