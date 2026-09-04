#!/usr/bin/env node

import crypto from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawn } from "node:child_process";

const MAX_PROJECT_CODE_LENGTH = 64;
const PROJECT_CODE_HASH_LENGTH = 16;

function projectCode(projectRoot) {
  const resolved = path.resolve(projectRoot);
  const legacy = resolved.replace(/[\\/]/g, "-").replace(/:/g, "");
  if (legacy.length <= MAX_PROJECT_CODE_LENGTH) return legacy;

  const hashInput = process.platform === "win32" ? resolved.toLowerCase() : resolved;
  const hash = crypto.createHash("sha256").update(hashInput).digest("hex").slice(0, PROJECT_CODE_HASH_LENGTH);
  const prefixLimit = MAX_PROJECT_CODE_LENGTH - PROJECT_CODE_HASH_LENGTH - 1;
  const basename = path.basename(resolved);
  const prefix =
    basename
      .replace(/[^A-Za-z0-9._-]/g, "-")
      .replace(/-+/g, "-")
      .replace(/^[-.]+|[-.]+$/g, "")
      .slice(0, prefixLimit)
      .replace(/[-.]+$/g, "") || "project";
  return `${prefix}-${hash}`;
}

function stateRoot() {
  return process.env.MACHINIST_DEEPCODE_HOME?.trim() || path.join(os.homedir(), ".deepcode");
}

function indexPath(projectRoot) {
  return path.join(stateRoot(), "projects", projectCode(projectRoot), "sessions-index.json");
}

function entries(projectRoot) {
  try {
    const document = JSON.parse(fs.readFileSync(indexPath(projectRoot), "utf8"));
    return Array.isArray(document?.entries) ? document.entries : [];
  } catch {
    return [];
  }
}

function fingerprints(projectRoot) {
  return Object.fromEntries(entries(projectRoot).map((entry) => [String(entry.id), String(entry.updateTime || "")]));
}

function newestChanged(projectRoot, baseline) {
  return entries(projectRoot)
    .filter((entry) => entry?.id && baseline[String(entry.id)] !== String(entry.updateTime || ""))
    .sort((left, right) => Date.parse(right.updateTime || right.createTime || 0) - Date.parse(left.updateTime || left.createTime || 0))[0];
}

function totalTokens(entry) {
  const usage = entry?.usage;
  if (!usage || typeof usage !== "object") return null;
  if (Number.isFinite(usage.total_tokens) && usage.total_tokens >= 0) return Math.trunc(usage.total_tokens);
  const input = Number.isFinite(usage.prompt_tokens) ? usage.prompt_tokens : 0;
  const output = Number.isFinite(usage.completion_tokens) ? usage.completion_tokens : 0;
  return input + output > 0 ? Math.trunc(input + output) : null;
}

function runHerdr(args) {
  const binary = process.env.HERDR_BIN_PATH?.trim() || "herdr";
  return new Promise((resolve) => {
    const child = spawn(binary, args, { env: process.env, stdio: "ignore" });
    child.once("error", () => resolve(false));
    child.once("exit", (code) => resolve(code === 0));
  });
}

async function observe(projectRoot) {
  if (process.env.HERDR_ENV !== "1" || !process.env.HERDR_PANE_ID?.trim()) return;
  const pane = process.env.HERDR_PANE_ID.trim();
  const source = "machinist:deepcode";
  const agent = "deepcode";
  const baseline = fingerprints(projectRoot);
  let sequence = 0;
  let lastSignature = "";
  let stopping = false;

  const report = async (state, entry, message = "") => {
    const sessionId = entry?.id ? String(entry.id) : "";
    const signature = `${state}\0${sessionId}\0${message}`;
    if (signature === lastSignature) return;
    lastSignature = signature;
    sequence += 1;
    await runHerdr([
      "pane",
      "report-agent",
      pane,
      "--source",
      source,
      "--agent",
      agent,
      "--state",
      state,
      "--seq",
      String(sequence),
      ...(sessionId ? ["--agent-session-id", sessionId] : []),
      ...(message ? ["--message", message] : []),
    ]);
  };

  const release = async () => {
    if (stopping) return;
    stopping = true;
    await runHerdr(["pane", "release-agent", pane, "--source", source, "--agent", agent]);
  };

  process.on("SIGINT", () => void release().finally(() => process.exit(0)));
  process.on("SIGTERM", () => void release().finally(() => process.exit(0)));

  await report("idle", null);
  await runHerdr([
    "pane",
    "report-metadata",
    pane,
    "--source",
    `${source}:display`,
    "--agent",
    agent,
    "--display-agent",
    "DeepCode",
    "--token",
    `model=${process.env.DEEPCODE_MODEL?.trim() || "default"}`,
  ]);

  while (!stopping) {
    const entry = newestChanged(projectRoot, baseline);
    if (!entry) {
      await report("idle", null);
    } else if (entry.status === "pending" || entry.status === "processing") {
      await report("working", entry);
    } else if (entry.status === "ask_permission" || entry.status === "waiting_for_user") {
      await report("blocked", entry, entry.status === "ask_permission" ? "Permission required" : "Answer required");
    } else {
      await report("idle", entry);
    }
    await new Promise((resolve) => setTimeout(resolve, 250));
  }
}

const [command, projectRoot = process.cwd(), argument = ""] = process.argv.slice(2);

if (command === "project-code") {
  process.stdout.write(`${projectCode(projectRoot)}\n`);
} else if (command === "snapshot") {
  process.stdout.write(`${JSON.stringify(fingerprints(projectRoot))}\n`);
} else if (command === "usage") {
  const entry = newestChanged(projectRoot, JSON.parse(argument || "{}"));
  const tokens = totalTokens(entry);
  if (tokens !== null && process.env.MACHINIST_TOKEN_USAGE_PATH?.trim()) {
    fs.writeFileSync(process.env.MACHINIST_TOKEN_USAGE_PATH, String(tokens), { mode: 0o600 });
  }
} else if (command === "observe") {
  await observe(projectRoot);
} else {
  process.stderr.write("usage: deepcode-session.mjs project-code|snapshot|usage|observe [PROJECT_ROOT] [SNAPSHOT]\n");
  process.exitCode = 2;
}
