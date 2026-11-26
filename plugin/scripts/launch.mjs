#!/usr/bin/env node
import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
import {
  chmodSync,
  existsSync,
  mkdirSync,
  readdirSync,
  readFileSync,
  renameSync,
  statSync,
  unlinkSync,
  writeFileSync,
} from "node:fs";
import { homedir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { gunzipSync, inflateRawSync } from "node:zlib";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const pluginRoot = path.resolve(__dirname, "..");
const binName = process.platform === "win32" ? "agy-ptc.exe" : "agy-ptc";

function readManifestMeta() {
  try {
    const raw = readFileSync(path.join(pluginRoot, "plugin.json"), "utf8");
    const m = JSON.parse(raw);
    return {
      version: typeof m.version === "string" ? m.version.trim() : "",
      repository:
        typeof m.repository === "string" && m.repository.trim()
          ? m.repository.trim().replace(/\.git$/i, "").replace(/\/+$/, "")
          : "https://github.com/Aliothmoon/agy-ptc",
    };
  } catch {
    return { version: "", repository: "https://github.com/Aliothmoon/agy-ptc" };
  }
}

function resolveDataDir(cliArgs) {
  for (let i = 0; i < cliArgs.length; i++) {
    if (cliArgs[i] === "--data-dir" && cliArgs[i + 1]) {
      return cliArgs[i + 1];
    }
    if (cliArgs[i].startsWith("--data-dir=")) {
      return cliArgs[i].slice("--data-dir=".length);
    }
  }
  const envData = (process.env.PLUGIN_DATA || "").trim();
  if (envData) return envData;
  return path.join(homedir(), ".gemini", "antigravity-cli", "plugin_data", "agy-ptc");
}

function findOnPath(exeName) {
  if (process.env.AGY_PTC_SKIP_PATH === "1") return "";
  const pathEnv = process.env.PATH || process.env.Path || "";
  for (const rawDir of pathEnv.split(path.delimiter)) {
    const dir = rawDir.trim().replace(/^"|"$/g, "");
    if (!dir) continue;
    const cand = path.join(dir, exeName);
    if (existsSync(cand)) return cand;
  }
  return "";
}

// Returns an immediately usable binary path, or "" when Marketplace first-run / version-upgrade bootstrap is needed.
function resolveExistingBinary(dataDir) {
  if (process.env.AGY_PTC_BIN && existsSync(process.env.AGY_PTC_BIN)) {
    return process.env.AGY_PTC_BIN;
  }
  if (process.env.AGY_PTC_SKIP_LOCAL !== "1") {
    const localCandidates = [
      path.join(__dirname, binName),
      path.resolve(__dirname, "..", "..", binName),
    ];
    for (const candidate of localCandidates) {
      if (existsSync(candidate)) {
        return candidate;
      }
    }
  }

  const { version: manifestVer } = readManifestMeta();
  const dataBin = path.join(dataDir, "bin", binName);
  const verStampPath = path.join(dataDir, "bin", ".version");
  if (existsSync(dataBin)) {
    let stampedVer = "";
    try {
      stampedVer = readFileSync(verStampPath, "utf8").trim();
    } catch {}
    if (!manifestVer || stampedVer === manifestVer) {
      return dataBin;
    }
  }

  const pathBin = findOnPath(binName);
  if (pathBin) return pathBin;

  return "";
}

function mapPlatformTarget() {
  const osMap = { win32: "windows", darwin: "darwin", linux: "linux" };
  const archMap = { x64: "amd64", arm64: "arm64" };
  const osName = osMap[process.platform];
  const archName = archMap[process.arch];
  if (!osName || !archName) {
    throw new Error(`unsupported platform ${process.platform}/${process.arch}`);
  }
  const ext = osName === "windows" ? ".zip" : ".tar.gz";
  return { osName, archName, ext };
}

function parseChecksumForAsset(checksumsText, assetName) {
  for (const rawLine of checksumsText.split(/\r?\n/)) {
    const line = rawLine.trim();
    if (!line) continue;
    const m = /^([0-9a-fA-F]{64})\s+\*?(.+)$/.exec(line);
    if (!m) continue;
    const file = path.posix.basename(m[2].trim().replace(/\\/g, "/"));
    if (file === assetName) {
      return m[1].toLowerCase();
    }
  }
  return "";
}

function extractBinaryFromZip(buf, targetBaseName) {
  const minEOCD = 22;
  const maxComment = 65535;
  let eocd = -1;
  for (let i = buf.length - minEOCD; i >= Math.max(0, buf.length - minEOCD - maxComment); i--) {
    if (buf.readUInt32LE(i) === 0x06054b50 && buf.readUInt16LE(i + 20) === buf.length - i - minEOCD) {
      eocd = i;
      break;
    }
  }
  if (eocd < 0) {
    throw new Error("invalid zip archive (missing EOCD)");
  }
  const totalEntries = buf.readUInt16LE(eocd + 10);
  let ptr = buf.readUInt32LE(eocd + 16);
  for (let idx = 0; idx < totalEntries && ptr + 46 <= buf.length; idx++) {
    if (buf.readUInt32LE(ptr) !== 0x02014b50) {
      throw new Error("invalid zip central directory header");
    }
    const method = buf.readUInt16LE(ptr + 10);
    const compSize = buf.readUInt32LE(ptr + 20);
    const uncompSize = buf.readUInt32LE(ptr + 24);
    const nameLen = buf.readUInt16LE(ptr + 28);
    const extraLen = buf.readUInt16LE(ptr + 30);
    const commentLen = buf.readUInt16LE(ptr + 32);
    const localOffset = buf.readUInt32LE(ptr + 42);
    const entryName = buf.subarray(ptr + 46, ptr + 46 + nameLen).toString("utf8").replace(/\\/g, "/");
    ptr += 46 + nameLen + extraLen + commentLen;

    const normalized = entryName.replace(/^\.\//, "");
    if (normalized === targetBaseName || (!normalized.includes("/") && path.posix.basename(normalized) === targetBaseName)) {
      if (localOffset + 30 > buf.length || buf.readUInt32LE(localOffset) !== 0x04034b50) {
        throw new Error("invalid zip local file header");
      }
      const lNameLen = buf.readUInt16LE(localOffset + 26);
      const lExtraLen = buf.readUInt16LE(localOffset + 28);
      const dataStart = localOffset + 30 + lNameLen + lExtraLen;
      const compSlice = buf.subarray(dataStart, dataStart + compSize);
      let out;
      if (method === 0) {
        out = Buffer.from(compSlice);
      } else if (method === 8) {
        out = inflateRawSync(compSlice);
      } else {
        throw new Error(`unsupported zip compression method ${method}`);
      }
      if (out.length !== uncompSize) {
        throw new Error(`zip size mismatch (${out.length} != ${uncompSize})`);
      }
      return out;
    }
  }
  throw new Error(`${targetBaseName} not found in zip archive`);
}

function extractBinaryFromTarGz(buf, targetBaseName) {
  const tar = gunzipSync(buf);
  let off = 0;
  while (off + 512 <= tar.length) {
    const hdr = tar.subarray(off, off + 512);
    if (hdr.every((b) => b === 0)) break;

    const rawName = hdr.subarray(0, 100).toString("utf8").replace(/\0.*$/, "");
    const prefix = hdr.subarray(345, 500).toString("utf8").replace(/\0.*$/, "");
    const sizeStr = hdr.subarray(124, 136).toString("ascii").replace(/\0.*$/, "").trim();
    const size = sizeStr ? parseInt(sizeStr, 8) : 0;
    const typeFlag = hdr[156];
    const dataStart = off + 512;
    const fullName = (prefix ? `${prefix}/${rawName}` : rawName).replace(/^\.\//, "");

    if ((typeFlag === 0 || typeFlag === 0x30) && fullName === targetBaseName) {
      return Buffer.from(tar.subarray(dataStart, dataStart + size));
    }
    off = dataStart + Math.ceil(size / 512) * 512;
  }
  throw new Error(`${targetBaseName} not found in tar.gz archive`);
}

async function fetchBufferWithTimeout(url, timeoutMs = 60000) {
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), timeoutMs);
  try {
    const res = await fetch(url, {
      redirect: "follow",
      signal: ctrl.signal,
      headers: { "User-Agent": "agy-ptc-plugin-bootstrap" },
    });
    if (!res.ok) {
      throw new Error(`HTTP ${res.status} fetching ${url}`);
    }
    return Buffer.from(await res.arrayBuffer());
  } finally {
    clearTimeout(timer);
  }
}

async function ensureBootstrappedBinary(dataDir) {
  const existing = resolveExistingBinary(dataDir);
  if (existing) return existing;

  const { version, repository } = readManifestMeta();
  if (!version) {
    throw new Error("missing version in plugin.json");
  }
  const { osName, archName, ext } = mapPlatformTarget();
  const assetName = `agy-ptc_${version}_${osName}_${archName}${ext}`;
  const baseUrl = (
    process.env.AGY_PTC_RELEASE_BASE_URL || `${repository}/releases/download/v${version}`
  ).replace(/\/+$/, "");

  const binDir = path.join(dataDir, "bin");
  const dstBin = path.join(binDir, binName);
  const verStampPath = path.join(binDir, ".version");

  try {
    mkdirSync(binDir, { recursive: true });
    const [checksumsRaw, archiveBuf] = await Promise.all([
      fetchBufferWithTimeout(`${baseUrl}/checksums.txt`, 20000),
      fetchBufferWithTimeout(`${baseUrl}/${assetName}`, 90000),
    ]);

    const expectedSha = parseChecksumForAsset(checksumsRaw.toString("utf8"), assetName);
    if (!expectedSha) {
      throw new Error(`asset ${assetName} missing from checksums.txt`);
    }
    const actualSha = createHash("sha256").update(archiveBuf).digest("hex").toLowerCase();
    if (actualSha !== expectedSha) {
      throw new Error(`SHA-256 mismatch for ${assetName} (expected ${expectedSha}, got ${actualSha})`);
    }

    const extracted =
      ext === ".zip"
        ? extractBinaryFromZip(archiveBuf, binName)
        : extractBinaryFromTarGz(archiveBuf, binName);

    const tmpFile = path.join(binDir, `.${binName}.tmp-${process.pid}-${Date.now()}`);
    writeFileSync(tmpFile, extracted, { mode: 0o755 });
    if (process.platform !== "win32") {
      chmodSync(tmpFile, 0o755);
    }

    if (existsSync(dstBin)) {
      const oldBackup = `${dstBin}.${process.pid}.old`;
      try {
        renameSync(dstBin, oldBackup);
        try {
          unlinkSync(oldBackup);
        } catch {}
      } catch {}
    }
    renameSync(tmpFile, dstBin);
    writeFileSync(verStampPath, `${version}\n`, "utf8");
    return dstBin;
  } catch (err) {
    // Offline fallback: if an older bootstrapped binary already exists in PLUGIN_DATA/bin, keep running instead of breaking.
    if (existsSync(dstBin) && !String(err?.message || "").includes("SHA-256 mismatch")) {
      process.stderr.write(`[agy-ptc] warning: update to v${version} skipped (${err.message}); using cached binary\n`);
      return dstBin;
    }
    throw err;
  }
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

async function runDirect(args) {
  const dataDir = resolveDataDir(args);
  let binPath = resolveExistingBinary(dataDir);
  if (!binPath) {
    // Do not block the 5s SessionStart hook on a cold ~15MB binary download; the MCP server handles bootstrap.
    if (args[0] === "hook") {
      process.stdout.write("{}\n");
      process.exit(0);
      return;
    }
    try {
      binPath = await ensureBootstrappedBinary(dataDir);
    } catch (err) {
      process.stderr.write(`agy-ptc bootstrap error: ${err.message}\n`);
      process.exit(1);
      return;
    }
  }

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

// Supervised MCP stdio proxy: keeps the host MCP pipe alive across binary hot-swaps,
// cold Marketplace binary bootstrap, and unexpected worker exits.
function runSupervisedMCP(args) {
  const dataDir = resolveDataDir(args);
  let shuttingDown = false;
  let hostUseContentLength = false;
  let savedInitParams = null;
  let sawInitializedNotif = false;

  let worker = null;
  let bootstrapping = null;
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

  function startWorker(needsReplay, explicitBinPath = "") {
    const binPath = explicitBinPath || resolveExistingBinary(dataDir) || binName;
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
    const latestBin = resolveExistingBinary(dataDir);
    if (!latestBin) return;
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

    startWorker(true, latestBin);
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
      const existingBin = resolveExistingBinary(dataDir);
      if (!existingBin) {
        pendingFrames.push({ raw, parsed, useContentLength: useCL });
        if (!bootstrapping) {
          bootstrapping = ensureBootstrappedBinary(dataDir)
            .then((downloadedBin) => {
              bootstrapping = null;
              if (shuttingDown || worker) return;
              const firstIsInit =
                pendingFrames.length > 0 &&
                pendingFrames[0].parsed &&
                !Array.isArray(pendingFrames[0].parsed) &&
                pendingFrames[0].parsed.method === "initialize";
              startWorker(savedInitParams !== null && !firstIsInit, downloadedBin);
              flushPendingToWorker();
            })
            .catch((err) => {
              bootstrapping = null;
              while (pendingFrames.length > 0) {
                const f = pendingFrames.shift();
                if (f.parsed) {
                  recordInFlightFromPayload(f.parsed, f.useContentLength);
                }
              }
              failInFlightRequests(
                `AgyPTC binary bootstrap failed (${err.message}); verify network access or install manually from https://github.com/Aliothmoon/agy-ptc/releases`,
              );
            });
        }
        return;
      }

      const replay =
        savedInitParams !== null &&
        !(parsed && !Array.isArray(parsed) && parsed.method === "initialize");
      if (crashTimes.length >= 5) {
        failInFlightRequests("PTC worker crashed repeatedly within 10s; run `agy-ptc doctor` to diagnose.");
        return;
      }
      startWorker(replay, existingBin);
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
