#!/usr/bin/env node
// Focused regression tests for withFileLocks + the shared per-process mutation
// queue in src/runtime/file-mutation.ts.
//
// What this covers:
//   - concurrent read-modify-write on disjoint files, nested inside withFileLocks
//   - writeIfUnchanged / writeTextPreservingBom re-enter without deadlock
//   - BOM preservation stays compatible when called under an outer lock
//   - symlink aliases resolve to the same canonical lock and serialize
//   - opposite input orders for the same file set do not deadlock
//   - an external callback throw releases every lock and the queue entry
//   - many sequential/concurrent writes leave no queue entries behind
//   - a delayed callback that inherited the AsyncLocalStorage store after the
//     outer operation ended must acquire the lock again, never bypass it
//
// The test compiles just this closure into an isolated temp directory so it
// never depends on a stale dist/. Fixtures and the temp build live under
// /tmp/opencode and are always removed.
//
// Usage: node scripts/test-file-mutation-locks.mjs

import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import {
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { readFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const SELF = fileURLToPath(import.meta.url);
const ROOT = dirname(dirname(SELF));
const TMP_ROOT = "/tmp/opencode";
mkdirSync(TMP_ROOT, { recursive: true });

const WORK_DIR = mkdtempSync(join(TMP_ROOT, "cave-file-locks-"));
const BUILD_DIR = join(WORK_DIR, "build");
const DATA_DIR = join(WORK_DIR, "data");
mkdirSync(DATA_DIR, { recursive: true });
process.env.CAVE_TOOLS_DATA_DIR = DATA_DIR;

let passed = 0;

function check(condition, message) {
  assert.ok(condition, message);
  passed += 1;
}

function equal(actual, expected, message) {
  assert.equal(actual, expected, message);
  passed += 1;
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function tick() {
  return new Promise((resolve) => setImmediate(resolve));
}

async function flushTicks(count = 8) {
  for (let index = 0; index < count; index += 1) await tick();
}

function withTimeout(promise, ms, label) {
  let timer;
  const timeout = new Promise((_, reject) => {
    timer = setTimeout(() => reject(new Error(`${label} timed out after ${ms}ms`)), ms);
  });
  return Promise.race([promise, timeout]).finally(() => clearTimeout(timer));
}

function buildClosure() {
  const result = spawnSync(
    "pnpm",
    [
      "exec",
      "tsc",
      "--outDir",
      BUILD_DIR,
      "--rootDir",
      join(ROOT, "src"),
      "--module",
      "NodeNext",
      "--moduleResolution",
      "NodeNext",
      "--target",
      "ES2022",
      "--strict",
      "--skipLibCheck",
      "--esModuleInterop",
      "--declaration",
      "false",
      "--sourceMap",
      "false",
      join(ROOT, "src", "runtime", "file-mutation.ts"),
    ],
    { cwd: ROOT, encoding: "utf8", timeout: 120000 },
  );
  if (result.status !== 0) {
    throw new Error(
      `strict closure compile failed (${result.status})\n${result.stdout ?? ""}${result.stderr ?? ""}`,
    );
  }
}

try {
  buildClosure();
  const mutation = await import(
    pathToFileURL(join(BUILD_DIR, "runtime", "file-mutation.js")).href
  );
  const {
    fileLockQueueSize,
    lockedWrite,
    withFileLocks,
    writeIfUnchanged,
    writeTextPreservingBom,
  } = mutation;

  console.log("1. concurrent read-modify-write on disjoint files");
  {
    const first = join(WORK_DIR, "disjoint-a.txt");
    const second = join(WORK_DIR, "disjoint-b.txt");
    writeFileSync(first, "alpha\n", "utf-8");
    writeFileSync(second, "bravo\n", "utf-8");

    await withTimeout(
      Promise.all([
        withFileLocks([first], async () => {
          const current = await readFile(first);
          await writeIfUnchanged(first, current, "alpha-1\n");
        }),
        withFileLocks([second], async () => {
          const current = await readFile(second);
          await writeIfUnchanged(second, current, "bravo-1\n");
        }),
      ]),
      5000,
      "disjoint read-modify-write",
    );

    equal(readFileSync(first, "utf8"), "alpha-1\n", "first disjoint edit preserved");
    equal(readFileSync(second, "utf8"), "bravo-1\n", "second disjoint edit preserved");
    equal(fileLockQueueSize(), 0, "queue empty after disjoint edits");
  }

  console.log("2. nested existing helpers + BOM compatibility");
  {
    const bomPath = join(WORK_DIR, "bom.txt");
    writeFileSync(bomPath, Buffer.concat([Buffer.from([0xef, 0xbb, 0xbf]), Buffer.from("bom\n")]));

    await withTimeout(
      withFileLocks([bomPath], async () => {
        await writeTextPreservingBom(bomPath, "changed\n");
      }),
      5000,
      "nested writeTextPreservingBom",
    );

    const bytes = readFileSync(bomPath);
    check(
      bytes[0] === 0xef && bytes[1] === 0xbb && bytes[2] === 0xbf,
      "nested writeTextPreservingBom keeps the UTF-8 BOM",
    );
    equal(bytes.subarray(3).toString("utf8"), "changed\n", "BOM file body updated");
    equal(fileLockQueueSize(), 0, "queue empty after nested BOM write");

    const optimisticPath = join(WORK_DIR, "optimistic.txt");
    writeFileSync(optimisticPath, "actual\n", "utf-8");
    let sawMismatch = false;
    try {
      await withFileLocks([optimisticPath], async () => {
        await writeIfUnchanged(optimisticPath, Buffer.from("stale\n"), "overwrite\n");
      });
    } catch (error) {
      sawMismatch = /File changed after it was read/.test(String(error?.message));
    }
    check(sawMismatch, "optimistic byte check still rejects stale expected bytes");
    equal(readFileSync(optimisticPath, "utf8"), "actual\n", "stale write left the file untouched");
    equal(fileLockQueueSize(), 0, "queue empty after optimistic mismatch");
  }

  console.log("3. symlink aliases resolve to one lock and serialize");
  {
    const target = join(WORK_DIR, "alias-target.txt");
    const alias = join(WORK_DIR, "alias-link.txt");
    writeFileSync(target, "alias-initial\n", "utf-8");
    symlinkSync(target, alias);

    await withTimeout(
      withFileLocks([alias], async () => {
        const current = await readFile(target);
        await writeIfUnchanged(target, current, "alias-inner\n");
      }),
      5000,
      "alias re-entry",
    );
    equal(readFileSync(target, "utf8"), "alias-inner\n", "alias re-entered the target lock");

    writeFileSync(target, "serial\n", "utf-8");
    const gate = deferred();
    const entered = deferred();
    let active = 0;
    let maxActive = 0;

    const holder = withFileLocks([target], async () => {
      active += 1;
      maxActive = Math.max(maxActive, active);
      entered.resolve();
      await gate.promise;
      active -= 1;
    });
    holder.catch(() => undefined);
    await withTimeout(entered.promise, 5000, "alias holder entered");

    let secondEntered = false;
    const second = withFileLocks([alias], async () => {
      secondEntered = true;
      active += 1;
      maxActive = Math.max(maxActive, active);
      active -= 1;
    });
    second.catch(() => undefined);

    await flushTicks(20);
    equal(secondEntered, false, "alias writer waits for the target lock");
    gate.resolve();
    await withTimeout(Promise.all([holder, second]), 5000, "alias serialization");
    equal(maxActive, 1, "target and alias never run concurrently");
  }

  console.log("4. opposite input order does not deadlock");
  {
    const left = join(WORK_DIR, "order-a.txt");
    const right = join(WORK_DIR, "order-b.txt");
    writeFileSync(left, "start-a\n", "utf-8");
    writeFileSync(right, "start-b\n", "utf-8");

    const writePair = (tag) =>
      withFileLocks([left, right], async () => {
        await lockedWrite(left, `${tag}-left\n`);
        await lockedWrite(right, `${tag}-right\n`);
      });
    const writePairReversed = (tag) =>
      withFileLocks([right, left], async () => {
        await lockedWrite(right, `${tag}-right\n`);
        await lockedWrite(left, `${tag}-left\n`);
      });

    await withTimeout(
      Promise.all([writePair("one"), writePairReversed("two")]),
      5000,
      "opposite-order acquisition",
    );

    const leftText = readFileSync(left, "utf8");
    const rightText = readFileSync(right, "utf8");
    check(
      (leftText === "one-left\n" && rightText === "one-right\n") ||
        (leftText === "two-left\n" && rightText === "two-right\n"),
      "opposite-order operations serialize as whole units without deadlock",
    );
    equal(fileLockQueueSize(), 0, "queue empty after opposite-order operations");
  }

  console.log("5. callback throw releases queued locks");
  {
    const throwPath = join(WORK_DIR, "throw.txt");
    writeFileSync(throwPath, "before\n", "utf-8");

    let sawBoom = false;
    try {
      await withFileLocks([throwPath], async () => {
        throw new Error("boom");
      });
    } catch (error) {
      sawBoom = /boom/.test(String(error?.message));
    }
    check(sawBoom, "outer callback rejection surfaced");
    equal(fileLockQueueSize(), 0, "queue entry removed after callback throw");

    await withTimeout(
      withFileLocks([throwPath], async () => {
        await writeIfUnchanged(throwPath, Buffer.from("before\n"), "after\n");
      }),
      5000,
      "write after callback throw",
    );
    equal(readFileSync(throwPath, "utf8"), "after\n", "lock usable after callback throw");

    // A waiter queued behind the failing operation must still run.
    writeFileSync(throwPath, "queued\n", "utf-8");
    const failed = withFileLocks([throwPath], async () => {
      throw new Error("first fails");
    });
    const queued = withFileLocks([throwPath], async () => {
      await lockedWrite(throwPath, "queued-ran\n");
    });
    const results = await withTimeout(
      Promise.allSettled([failed, queued]),
      5000,
      "queued waiter after failure",
    );
    equal(results[0].status, "rejected", "first operation rejected");
    equal(results[1].status, "fulfilled", "queued operation ran after the failure");
    equal(readFileSync(throwPath, "utf8"), "queued-ran\n", "queued write applied");
    equal(fileLockQueueSize(), 0, "queue empty after failed + queued operations");
  }

  console.log("6. many writes leave the queue bounded");
  {
    const queuePath = join(WORK_DIR, "queue.txt");
    writeFileSync(queuePath, "v0\n", "utf-8");

    let current = Buffer.from("v0\n");
    for (let index = 0; index < 150; index += 1) {
      const next = `v${index + 1}\n`;
      await withFileLocks([queuePath], async () => {
        await writeIfUnchanged(queuePath, current, next);
      });
      current = Buffer.from(next);
    }
    equal(readFileSync(queuePath, "utf8"), "v150\n", "sequential writes all landed");
    equal(fileLockQueueSize(), 0, "queue empty after sequential writes");

    await withTimeout(
      Promise.all(
        Array.from({ length: 100 }, (_, index) =>
          withFileLocks([queuePath], async () => {
            await lockedWrite(queuePath, `concurrent-${index}\n`);
          }),
        ),
      ),
      10000,
      "concurrent queued writes",
    );
    check(/^concurrent-\d+\n$/.test(readFileSync(queuePath, "utf8")), "concurrent write landed");
    equal(fileLockQueueSize(), 0, "queue empty after concurrent writes");
  }

  console.log("7. delayed inherited callback must reacquire");
  {
    const delayedPath = join(WORK_DIR, "delayed.txt");
    writeFileSync(delayedPath, "base\n", "utf-8");

    const gate = deferred();
    const scheduled = deferred();
    const holderEntered = deferred();
    const holderRelease = deferred();
    let delayedAttempted = false;
    let delayedDone = false;
    let delayedTask;

    await withFileLocks([delayedPath], async () => {
      setImmediate(() => {
        delayedTask = (async () => {
          await gate.promise;
          delayedAttempted = true;
          await writeIfUnchanged(delayedPath, Buffer.from("base\n"), "delayed\n");
          delayedDone = true;
        })();
        delayedTask.catch(() => undefined);
        scheduled.resolve();
      });
    });

    const holder = withFileLocks([delayedPath], async () => {
      holderEntered.resolve();
      await holderRelease.promise;
    });
    holder.catch(() => undefined);

    await withTimeout(holderEntered.promise, 5000, "holder acquired");
    await withTimeout(scheduled.promise, 5000, "delayed callback scheduled");
    gate.resolve();
    await flushTicks(20);

    check(delayedAttempted, "delayed callback attempted its write");
    equal(delayedDone, false, "delayed callback must wait instead of bypassing the lock");

    holderRelease.resolve();
    await withTimeout(holder, 5000, "holder release");
    await withTimeout(delayedTask, 5000, "delayed callback completion");
    equal(readFileSync(delayedPath, "utf8"), "delayed\n", "delayed callback wrote after reacquiring");
    equal(fileLockQueueSize(), 0, "queue empty after delayed callback");
  }

  console.log("8. incremental nested acquisition is rejected, then cleaned up");
  {
    const outerPath = join(WORK_DIR, "outer.txt");
    const innerPath = join(WORK_DIR, "inner.txt");
    writeFileSync(outerPath, "outer\n", "utf-8");
    writeFileSync(innerPath, "inner\n", "utf-8");

    let sawHelpfulError = false;
    try {
      await withFileLocks([outerPath], async () => {
        await withFileLocks([innerPath], async () => {
          throw new Error("should not reach nested operation");
        });
      });
    } catch (error) {
      sawHelpfulError = /Acquire every path up front/.test(String(error?.message));
    }
    check(sawHelpfulError, "out-of-order nested acquisition reports a useful error");
    equal(fileLockQueueSize(), 0, "queue empty after rejected nested acquisition");

    await withTimeout(
      withFileLocks([outerPath, innerPath], async () => {
        await lockedWrite(outerPath, "outer-ok\n");
        await lockedWrite(innerPath, "inner-ok\n");
      }),
      5000,
      "both paths after rejection",
    );
    equal(readFileSync(outerPath, "utf8"), "outer-ok\n", "outer path usable after rejection");
    equal(readFileSync(innerPath, "utf8"), "inner-ok\n", "inner path usable after rejection");
  }

  console.log(`\nAll ${passed} checks passed.`);
} catch (error) {
  console.error("\nfile-mutation lock test failed:");
  console.error(error?.stack ?? error);
  process.exitCode = 1;
} finally {
  rmSync(WORK_DIR, { recursive: true, force: true });
}
