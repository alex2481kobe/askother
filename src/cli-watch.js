// `orca-cli.js watch` — the missing half of "wakes its orchestrator".
//
// Context (docs/agent-orchestrator-skill.md, ROLE_INSTRUCTIONS.orchestrator in
// src/agent-tools/role-instructions.js): commit bb416e6 made a completed lane
// enqueue a durable wakeup (registry-lane-terminal.js -> enqueueAgentEvent),
// exactly like a failed or stopped one already did. That queue is real and
// drainable (`event.drain`), but draining is a PULL: something has to call it.
// A Claude Code orchestrator is only "woken" by its own tool output — a
// completed lane sitting in the queue does not interrupt it or inject a
// message. So the enqueue was necessary but not sufficient: nothing was ever
// calling `event.drain` (or re-polling `lane.list`) on a cadence, which is
// exactly the reported symptom (a lane finished at 17:01 and the orchestrator
// found out only when asked).
//
// Why this polls `lane.list` instead of the SSE stream
// (`GET /api/streams/events`, src/event-streams.js): that stream is a
// revision-only change signal for the dashboard's own re-fetch — no lane
// bodies, no state, no title — and `hasStreamAuth` (src/server.js) requires
// operator auth (an API token or a paired browser session), not the tool
// lease or bare-loopback-admin posture a CLI orchestrator process normally
// has. `GET /api/orchestrators/{id}/lanes` is the exact same non-mutating,
// already-authenticated route `lane.list` already polls, so `watch` just
// automates that poll on a short interval and prints only the lines that
// changed — the simplest mechanism that actually reaches a Claude Code
// orchestrator: run it under the Monitor tool, and each printed line is a
// stdout notification that wakes the session.
//
// This module is pure-logic-first (diffLanes / isTerminalLaneState /
// hasActiveLane are exported and unit-testable without a network) with a thin
// I/O loop (runWatch) around it.

import { resolveApiToken } from './api-token.js';
import { DEFAULT_BASE_URL, networkCode } from './mcp-connection.js';

const DEFAULT_INTERVAL_MS = 2000;
const HTTP_TIMEOUT_MS = 5000;

// Mirrors the TERMINAL set in registry-overview.js (not exported there), which
// mirrors src/worker-contract.js LANE_STATES. Kept as plain strings here so
// this module has no dependency on the daemon's registry internals — it only
// ever sees lanes over HTTP.
const TERMINAL_STATES = new Set(['accepted', 'blocked', 'archived', 'stopped', 'done', 'failed']);

export function isTerminalLaneState(state) {
  return TERMINAL_STATES.has(String(state || '').toLowerCase());
}

export function hasActiveLane(lanes) {
  return (lanes || []).some((lane) => !isTerminalLaneState(lane.state));
}

function formatChange(id, title, from, to) {
  return `${String(id).slice(0, 8)} ${title || '(untitled)'} ${from}→${to}`;
}

// Pure diff: given the previous snapshot (Map laneId -> state) and the lanes
// just fetched, returns the change lines to print and the new snapshot to
// remember. The very first poll (`seenBefore: false`) only seeds the
// snapshot — a baseline is not a change, so nothing is printed for lanes that
// were already running before `watch` started. A lane id present now but
// absent from `previous` on a LATER poll is newly spawned; it prints as
// `spawned→<state>` rather than being silently absorbed, because a lane
// appearing mid-watch is itself news.
export function diffLanes(previous, lanes, { seenBefore }) {
  const lines = [];
  const next = new Map();
  for (const lane of lanes || []) {
    const id = String(lane?.id || '');
    if (!id) continue;
    const title = String(lane?.title || '(untitled)');
    const state = String(lane?.state || 'unknown');
    next.set(id, state);
    if (!seenBefore) continue;
    const prior = previous.get(id);
    if (prior === undefined) {
      lines.push(formatChange(id, title, 'spawned', state));
    } else if (prior !== state) {
      lines.push(formatChange(id, title, prior, state));
    }
  }
  return { lines, next };
}

async function fetchLanes(baseUrl, orchestratorId, headers) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), HTTP_TIMEOUT_MS);
  try {
    const res = await fetch(`${baseUrl}/api/orchestrators/${encodeURIComponent(orchestratorId)}/lanes`, {
      headers: { accept: 'application/json', ...headers },
      signal: controller.signal,
    });
    const text = await res.text();
    let json = null;
    try { json = JSON.parse(text); } catch { json = null; }
    return { status: res.status, json };
  } catch (error) {
    return { networkError: error };
  } finally {
    clearTimeout(timer);
  }
}

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

export const WATCH_USAGE = 'watch --orchestrator <id> [--url URL] [--interval MS] [--forever]';

// Runs until the orchestrator has no active lane left (any lane not in a
// terminal state), or forever with --forever, or until SIGINT/SIGTERM. Prints
// exactly one line per real lane state change; a daemon outage prints one line
// when first detected and one line on reconnect, then stays silent while
// retrying. Never prints a token: the API token (if any) only ever goes in a
// request header.
export async function runWatch(flags, out) {
  const orchestratorId = String(flags.orchestrator || '').trim();
  if (!orchestratorId) {
    out.err(`watch needs --orchestrator <id>.\nUsage: ${WATCH_USAGE}`);
    return 2;
  }
  const baseUrl = String(flags.url || process.env.ORCA_AGENT_TOOLS_BASE_URL || DEFAULT_BASE_URL).replace(/\/$/, '');
  const intervalMs = Math.max(250, Number.parseInt(flags.interval, 10) || DEFAULT_INTERVAL_MS);
  const forever = Boolean(flags.forever);

  const apiToken = resolveApiToken(process.env);
  if (apiToken.error) {
    out.err(apiToken.error);
    return 1;
  }
  const headers = {};
  if (apiToken.token) headers['x-orca-token'] = apiToken.token;

  let previous = new Map();
  let seenBefore = false;
  let everHadLane = false;
  let outage = false;

  let stopped = false;
  const requestStop = () => { stopped = true; };
  process.once('SIGINT', requestStop);
  process.once('SIGTERM', requestStop);

  try {
    for (;;) {
      const res = await fetchLanes(baseUrl, orchestratorId, headers);
      if (res.networkError) {
        if (!outage) {
          outage = true;
          out.log(`[watch] daemon unreachable at ${baseUrl} (${networkCode(res.networkError) || 'connection error'}); retrying`);
        }
      } else if (res.status !== 200 || !Array.isArray(res.json)) {
        if (res.status === 404) {
          out.err(`[watch] orchestrator ${orchestratorId} not found at ${baseUrl}.`);
          return 1;
        }
        if (!outage) {
          outage = true;
          out.log(`[watch] daemon at ${baseUrl} answered HTTP ${res.status}; retrying`);
        }
      } else {
        if (outage) {
          outage = false;
          out.log(`[watch] daemon reconnected at ${baseUrl}`);
        }
        const lanes = res.json;
        const { lines, next } = diffLanes(previous, lanes, { seenBefore });
        for (const line of lines) out.log(line);
        previous = next;
        seenBefore = true;
        if (lanes.length > 0) everHadLane = true;
        if (!forever && everHadLane && !hasActiveLane(lanes)) {
          return 0;
        }
      }
      if (stopped) return 0;
      await sleep(intervalMs);
      if (stopped) return 0;
    }
  } finally {
    process.removeListener('SIGINT', requestStop);
    process.removeListener('SIGTERM', requestStop);
  }
}
