// `orca-cli.js watch` — the CLI half of "a lane that finishes wakes its
// orchestrator" (see commit bb416e6, which fixed the enqueue side: a completed
// lane now pushes a durable wakeup). That queue is a PULL (`event.drain`), and
// nothing was ever pulling it on a cadence for a Claude Code orchestrator,
// which is only woken by its own tool output. `watch` is that cadence: it
// polls the same non-mutating `lane.list` route an orchestrator already uses
// and prints one line per real lane state change.
//
// Two layers: `diffLanes` / `isTerminalLaneState` / `hasActiveLane` are pure
// and tested directly (no network, no process). `runWatch` is tested end to
// end against a stub HTTP server standing in for Orca, the same pattern
// test/mcp-connection-errors.test.js uses for the MCP bridge.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import http from 'node:http';
import path from 'node:path';
import test from 'node:test';

import { ROOT, cleanChildEnv } from './helpers/bridge-client.js';
import { diffLanes, hasActiveLane, isTerminalLaneState } from '../src/cli-watch.js';

const CLI = path.join(ROOT, 'src', 'orca-cli.js');

// ---- pure logic -----------------------------------------------------------

test('isTerminalLaneState / hasActiveLane classify the documented terminal set', () => {
  for (const state of ['accepted', 'blocked', 'archived', 'stopped', 'done', 'failed', 'DONE']) {
    assert.equal(isTerminalLaneState(state), true, state);
  }
  for (const state of ['queued', 'starting', 'running', 'ready_for_audit', 'auditing', 'fix_requested', '']) {
    assert.equal(isTerminalLaneState(state), false, state);
  }
  assert.equal(hasActiveLane([{ state: 'done' }, { state: 'failed' }]), false);
  assert.equal(hasActiveLane([{ state: 'done' }, { state: 'running' }]), true);
  assert.equal(hasActiveLane([]), false);
});

test('diffLanes seeds silently on the first (baseline) poll', () => {
  const lanes = [{ id: 'lane_aaaaaaaaaaaa', title: 'scout', state: 'running' }];
  const { lines, next } = diffLanes(new Map(), lanes, { seenBefore: false });
  assert.deepEqual(lines, [], 'a baseline is not a change');
  assert.equal(next.get('lane_aaaaaaaaaaaa'), 'running');
});

test('diffLanes reports exactly one line per real state change, in the exact format', () => {
  const first = [{ id: 'lane_aaaaaaaaaaaa', title: 'scout', state: 'running' }];
  const seed = diffLanes(new Map(), first, { seenBefore: false });
  const second = [{ id: 'lane_aaaaaaaaaaaa', title: 'scout', state: 'done' }];
  const { lines, next } = diffLanes(seed.next, second, { seenBefore: true });
  assert.deepEqual(lines, ['lane_aaa scout running→done']);
  assert.equal(next.get('lane_aaaaaaaaaaaa'), 'done');
});

test('diffLanes stays silent when nothing changed', () => {
  const lanes = [{ id: 'lane_bbbbbbbbbbbb', title: 'fix', state: 'running' }];
  const seed = diffLanes(new Map(), lanes, { seenBefore: false });
  const { lines } = diffLanes(seed.next, lanes, { seenBefore: true });
  assert.deepEqual(lines, []);
});

test('diffLanes reports a lane that appears mid-watch as spawned→<state>', () => {
  const seed = diffLanes(new Map(), [], { seenBefore: false });
  const lanes = [{ id: 'lane_cccccccccccc', title: 'new work', state: 'queued' }];
  const { lines } = diffLanes(seed.next, lanes, { seenBefore: true });
  assert.deepEqual(lines, ['lane_ccc new work spawned→queued']);
});

// ---- end to end: the real CLI against a stub daemon ------------------------

function startStub(responder) {
  const server = http.createServer((req, res) => {
    responder(req, res);
  });
  return new Promise((resolve) => {
    server.listen(0, '127.0.0.1', () => {
      resolve({ base: `http://127.0.0.1:${server.address().port}`, close: () => new Promise((done) => server.close(done)) });
    });
  });
}

function sendLanes(res, lanes) {
  res.writeHead(200, { 'content-type': 'application/json' });
  res.end(JSON.stringify(lanes));
}

function runCli(args, env) {
  const child = spawn(process.execPath, [CLI, ...args], {
    cwd: ROOT,
    env: { ...cleanChildEnv(), ...env },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  let stdout = '';
  let stderr = '';
  child.stdout.on('data', (chunk) => { stdout += chunk.toString(); });
  child.stderr.on('data', (chunk) => { stderr += chunk.toString(); });
  const exit = new Promise((resolve) => child.on('exit', (code) => resolve(code)));
  return { child, exit, lines: () => stdout.split('\n').filter(Boolean), stderrText: () => stderr };
}

test('watch prints one change line then exits once the lane reaches a terminal state', async () => {
  let poll = 0;
  const stub = await startStub((req, res) => {
    poll += 1;
    if (poll === 1) return sendLanes(res, [{ id: 'lane_deadbeef0000', title: 'scout the session flow', state: 'running' }]);
    return sendLanes(res, [{ id: 'lane_deadbeef0000', title: 'scout the session flow', state: 'done' }]);
  });
  try {
    const run = runCli(['watch', '--orchestrator', 'orc_test', '--url', stub.base, '--interval', '50'], {});
    const code = await run.exit;
    assert.equal(code, 0);
    assert.deepEqual(run.lines(), ['lane_dea scout the session flow running→done']);
  } finally {
    await stub.close();
  }
});

test('watch stays silent across unchanged polls', async () => {
  const stub = await startStub((req, res) => {
    sendLanes(res, [{ id: 'lane_stillrunning0', title: 'long job', state: 'running' }]);
  });
  try {
    const run = runCli(['watch', '--orchestrator', 'orc_test', '--url', stub.base, '--interval', '40'], {});
    await new Promise((resolve) => setTimeout(resolve, 250));
    run.child.kill('SIGTERM');
    await run.exit;
    assert.deepEqual(run.lines(), [], 'no line printed while the lane never changes state');
  } finally {
    await stub.close();
  }
});

test('watch reports an outage once, then a reconnect once, without ever printing a token', async () => {
  let calls = 0;
  const stub = await startStub((req, res) => {
    calls += 1;
    if (calls <= 2) {
      req.socket.destroy();
      return;
    }
    sendLanes(res, [{ id: 'lane_flaky00000000', title: 'flaky lane', state: 'done' }]);
  });
  try {
    const run = runCli(['watch', '--orchestrator', 'orc_test', '--url', stub.base, '--interval', '40'], { ORCA_API_TOKEN: 'sekret-token-value' });
    const code = await run.exit;
    assert.equal(code, 0);
    const lines = run.lines();
    assert.equal(lines.filter((line) => line.includes('unreachable')).length, 1, 'exactly one outage line');
    assert.equal(lines.filter((line) => line.includes('reconnected')).length, 1, 'exactly one reconnect line');
    for (const line of lines) assert.ok(!line.includes('sekret-token-value'), 'never prints the token');
  } finally {
    await stub.close();
  }
});

test('watch --forever keeps running past a terminal state until killed', async () => {
  const stub = await startStub((req, res) => {
    sendLanes(res, [{ id: 'lane_donealready000', title: 'already done', state: 'done' }]);
  });
  try {
    const run = runCli(['watch', '--orchestrator', 'orc_test', '--url', stub.base, '--interval', '40', '--forever'], {});
    await new Promise((resolve) => setTimeout(resolve, 200));
    assert.equal(run.child.exitCode, null, 'still running with --forever even though the only lane is terminal');
    run.child.kill('SIGTERM');
    const code = await run.exit;
    assert.equal(code, 0);
  } finally {
    await stub.close();
  }
});

test('watch requires --orchestrator', async () => {
  const run = runCli(['watch', '--url', 'http://127.0.0.1:1'], {});
  const code = await run.exit;
  assert.equal(code, 2);
  assert.match(run.stderrText(), /--orchestrator/);
});
