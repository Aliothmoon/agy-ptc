#!/usr/bin/env node
import { spawn } from "node:child_process";
import { existsSync, readdirSync, statSync, unlinkSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const binName = process.platform === "win32" ? "agy-ptc.exe" : "agy-ptc";

function resolveBinary() {
  if (process.env.AGY_PTC_BIN && existsSync(process.env.AGY_PTC_BIN)) {
    return process.env.AGY_PTC_BIN;
  }
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

function cleanupOldBinaries(binPath) {
  try {
    const dir = path.dirname(binPath);
    if (!existsSync(dir)) return;
    for (const name of readdirSync(dir)) {
      if (name.endsWith(".old")) {
        try {
          unlinkSync(path.join(dir, name));
        } catch {}
      }
    }
  } catch {}
}

function statSignature(filePath) {
  try {
    const st = statSync(filePath);
    return `${st.mtimeMs}:${st.size}`;
  } catch {
    return "";
  }
}

const MAX_FRAME_BYTES = 16 * 1024 * 1024;

function createRpcFramer(onFrame) {
  let buf = Buffer.alloc(0);
  let skipBytes = 0;

  return function push(chunk) {
    buf = buf.length === 0 ? chunk : Buffer.concat([buf, chunk]);

    while (buf.length > 0) {
      if (skipBytes > 0) {
        const drop = Math.min(skipBytes, buf.length);
        buf = buf.subarray(drop);
        skipBytes -= drop;
        if (skipBytes > 0) break;
        continue;
      }

      let trim = 0;
      while (trim < buf.length && (buf[trim] === 0x0a || buf[trim] === 0x0d)) {
        trim++;
      }
      if (trim > 0) {
        buf = buf.subarray(trim);
        if (buf.length === 0) break;
      }

      const probe = buf.subarray(0, Math.min(buf.length, 16)).toString("ascii").toLowerCase();
      if (probe.startsWith("content-length:") || probe.startsWith("content-type:")) {
        let sepIdx = buf.indexOf("\r\n\r\n");
        let sepLen = 4;
        if (sepIdx < 0) {
          sepIdx = buf.indexOf("\n\n");
          sepLen = 2;
        }
        if (sepIdx < 0) {
          if (buf.length > 16384) buf = Buffer.alloc(0);
          break;
        }
        const header = buf.subarray(0, sepIdx).toString("ascii");
        const m = /content-length\s*:\s*(\d+)/i.exec(header);
        const len = m ? Number(m[1]) : -1;
        if (len < 0 || len > MAX_FRAME_BYTES) {
          buf = buf.subarray(sepIdx + sepLen);
          if (len > MAX_FRAME_BYTES) skipBytes = len;
          continue;
        }
        if (buf.length < sepIdx + sepLen + len) {
          break;
        }
        const body = buf.subarray(sepIdx + sepLen, sepIdx + sepLen + len).toString("utf8");
        buf = buf.subarray(sepIdx + sepLen + len);
        if (body.trim()) onFrame(body, true);
        continue;
      }

      const nl = buf.indexOf(0x0a);
      if (nl < 0) {
        if (buf.length > MAX_FRAME_BYTES) buf = Buffer.alloc(0);
        break;
      }
      let end = nl;
      if (end > 0 && buf[end - 1] === 0x0d) end--;
      const line = buf.subarray(0, end).toString("utf8");
      buf = buf.subarray(nl + 1);
      if (line.trim()) onFrame(line, false);
    }
  };
}

function writeFrame(stream, rawJson, useContentLength) {
  if (!stream || stream.destroyed || !stream.writable) return false;
  if (useContentLength) {
    const byteLen = Buffer.byteLength(rawJson, "utf8");
    stream.write(`Content-Length: ${byteLen}\r\n\r\n${rawJson}`);
  } else {
    stream.write(`${rawJson}\n`);
  }
  return true;
}

function idKey(id) {
  return id === undefined ? "" : JSON.stringify(id);
}

const REINIT_ID = "__agy_ptc_supervisor_reinit__";

function runDirect(args) {
  const binPath = resolveBinary();
  const child = spawn(binPath, args, {
    stdio: "inherit",
    windowsHide: true,
  });

  child.on("error", (err) => {
    if (args[0] === "hook") {
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
}

// Supervised MCP stdio proxy: keeps the host MCP pipe alive across `agy-ptc install`
// binary hot-swaps (reloading cleanly while idle) and unexpected worker exits.
function runSupervisedMCP(args) {
  let shuttingDown = false;
  let hostUseContentLength = false;
  let savedInitParams = null;
  let sawInitializedNotif = false;

  let worker = null;
  const pendingFrames = [];
  const inFlight = new Map(); // idKey -> { id, method, useContentLength }
  const crashTimes = [];

  for (const s of [process.stdout, process.stderr]) {
    s?.on?.("error", (err) => {
      if (err?.code === "EPIPE") shutdown();
    });
  }

  function recordInFlightFromPayload(parsed, useContentLength) {
    const items = Array.isArray(parsed) ? parsed : [parsed];
    for (const item of items) {
      if (item && typeof item === "object" && item.id !== undefined && item.id !== null && item.method) {
        inFlight.set(idKey(item.id), {
          id: item.id,
          method: item.method,
          useContentLength,
        });
      }
    }
  }

  function clearInFlightFromResponse(parsed) {
    const items = Array.isArray(parsed) ? parsed : [parsed];
    for (const item of items) {
      if (item && typeof item === "object" && item.id !== undefined) {
        inFlight.delete(idKey(item.id));
      }
    }
  }

  function flushPendingToWorker() {
    if (!worker || !worker.ready) return;
    while (pendingFrames.length > 0) {
      const f = pendingFrames.shift();
      if (f.parsed) {
        recordInFlightFromPayload(f.parsed, f.useContentLength);
      }
      writeFrame(worker.proc.stdin, f.raw, f.useContentLength);
    }
  }

  function failInFlightRequests(reason) {
    if (inFlight.size === 0) return;
    for (const entry of inFlight.values()) {
      let resp;
      if (entry.method === "tools/call") {
        resp = {
          jsonrpc: "2.0",
          id: entry.id,
          result: {
            content: [{ type: "text", text: reason }],
            isError: true,
          },
        };
      } else {
        resp = {
          jsonrpc: "2.0",
          id: entry.id,
          error: { code: -32603, message: reason },
        };
      }
      writeFrame(process.stdout, JSON.stringify(resp), entry.useContentLength);
    }
    inFlight.clear();
  }

  function startWorker(needsReplay) {
    const binPath = resolveBinary();
    cleanupOldBinaries(binPath);
    const sig = statSignature(binPath);

    const proc = spawn(binPath, args, {
      stdio: ["pipe", "pipe", "inherit"],
      windowsHide: true,
    });

    const w = {
      proc,
      binPath,
      sig,
      ready: !needsReplay,
      replaying: needsReplay,
      closingForReload: false,
    };
    worker = w;

    const parseOut = createRpcFramer((raw, useCL) => {
      let parsed = null;
      try {
        parsed = JSON.parse(raw);
      } catch {}

      if (w.replaying && parsed && !Array.isArray(parsed) && parsed.id === REINIT_ID) {
        w.replaying = false;
        w.ready = true;
        if (sawInitializedNotif) {
          writeFrame(
            proc.stdin,
            JSON.stringify({ jsonrpc: "2.0", method: "notifications/initialized" }),
            hostUseContentLength,
          );
        }
        flushPendingToWorker();
        return;
      }

      if (parsed) {
        clearInFlightFromResponse(parsed);
        crashTimes.length = 0;
      }
      writeFrame(process.stdout, raw, hostUseContentLength || useCL);
    });

    proc.stdout.on("data", parseOut);

    proc.on("error", (err) => {
      if (worker === w) worker = null;
      if (shuttingDown || w.closingForReload) return;
      process.stderr.write(`agy-ptc worker spawn error (${binPath}): ${err.message}\n`);
      failInFlightRequests(`PTC worker spawn error (${err.message}); check agy-ptc installation.`);
      process.exit(1);
    });

    proc.on("exit", (code) => {
      if (w.closingForReload) {
        cleanupOldBinaries(w.binPath);
        return;
      }
      if (worker === w) worker = null;
      if (shuttingDown) {
        process.exit(0);
      }

      const now = Date.now();
      crashTimes.push(now);
      while (crashTimes.length > 0 && now - crashTimes[0] > 10000) {
        crashTimes.shift();
      }
      failInFlightRequests(
        `PTC worker exited unexpectedly (code=${code ?? "signal"}) and was automatically restarted; please retry the call.`,
      );
      if (crashTimes.length >= 5 && savedInitParams === null) {
        process.exit(code ?? 1);
      }
      if (pendingFrames.length > 0 && crashTimes.length < 5) {
        startWorker(savedInitParams !== null);
        flushPendingToWorker();
      }
    });

    if (needsReplay) {
      const initMsg = JSON.stringify({
        jsonrpc: "2.0",
        id: REINIT_ID,
        method: "initialize",
        params: savedInitParams ?? {},
      });
      writeFrame(proc.stdin, initMsg, hostUseContentLength);
    }
  }

  function maybeHotReloadIdleWorker() {
    if (!worker || !worker.ready || inFlight.size > 0 || savedInitParams === null) {
      return;
    }
    const latestBin = resolveBinary();
    const latestSig = statSignature(latestBin);
    if (latestBin === worker.binPath && latestSig === worker.sig) {
      return;
    }
    const old = worker;
    old.closingForReload = true;
    worker = null;
    try {
      old.proc.stdin.end();
    } catch {}
    const killTimer = setTimeout(() => {
      try {
        old.proc.kill();
      } catch {}
    }, 2000);
    killTimer.unref?.();

    startWorker(true);
  }

  const parseHostIn = createRpcFramer((raw, useCL) => {
    if (shuttingDown) return;
    hostUseContentLength = useCL;

    let parsed = null;
    try {
      parsed = JSON.parse(raw);
    } catch {}

    if (parsed && !Array.isArray(parsed)) {
      if (parsed.method === "initialize") {
        savedInitParams = parsed.params ?? {};
      } else if (parsed.method === "notifications/initialized") {
        sawInitializedNotif = true;
      }
    }

    maybeHotReloadIdleWorker();

    if (!worker) {
      const replay =
        savedInitParams !== null &&
        !(parsed && !Array.isArray(parsed) && parsed.method === "initialize");
      if (crashTimes.length >= 5) {
        failInFlightRequests("PTC worker crashed repeatedly within 10s; run `agy-ptc doctor` to diagnose.");
        return;
      }
      startWorker(replay);
    }

    if (!worker.ready) {
      pendingFrames.push({ raw, parsed, useContentLength: useCL });
      return;
    }

    if (parsed) {
      recordInFlightFromPayload(parsed, useCL);
    }
    writeFrame(worker.proc.stdin, raw, useCL);
  });

  function shutdown() {
    if (shuttingDown) return;
    shuttingDown = true;
    if (!worker) {
      process.exit(0);
    }
    try {
      worker.proc.stdin.end();
    } catch {}
    const forceTimer = setTimeout(() => {
      try {
        worker?.proc?.kill();
      } catch {}
      process.exit(0);
    }, 1500);
    forceTimer.unref?.();
  }

  process.stdin.on("data", parseHostIn);
  process.stdin.on("end", shutdown);
  process.stdin.on("close", shutdown);
  process.on("SIGINT", shutdown);
  process.on("SIGTERM", shutdown);
}

const subArgs = process.argv.slice(2);
if (subArgs[0] === "mcp") {
  runSupervisedMCP(subArgs);
} else {
  runDirect(subArgs);
}
