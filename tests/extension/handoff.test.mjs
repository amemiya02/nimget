import assert from "node:assert/strict";
import test from "node:test";
import { lookupHandoffReceipt, submitHandoff } from "../../extension/handoff.js";

const handoffId = "06278ae9-f42b-44e4-8384-e9d449af4d5e";
const request = { handoffId, url: "https://example.test/archive.zip" };

test("a handoff requires a durable receipt with a task ID", async (t) => {
  const transport = t.mock.method(globalThis, "fetch", async () => Response.json({ taskId: "saved" }, { status: 201 }));
  assert.equal(await submitHandoff(request, "fixture-token"), "saved");

  transport.mock.mockImplementation(async () => Response.json({ taskId: "saved" }, { status: 200 }));
  assert.equal(await submitHandoff(request, "fixture-token"), "saved");
  assert.equal((await lookupHandoffReceipt(handoffId, "fixture-token")).taskId, "saved");

  transport.mock.mockImplementation(async () => Response.json({ taskId: "pending" }, { status: 202 }));
  await assert.rejects(submitHandoff(request, "fixture-token"), /UNEXPECTED_RESPONSE_STATUS/);
  await assert.rejects(lookupHandoffReceipt(handoffId, "fixture-token"), /UNEXPECTED_RESPONSE_STATUS/);

  transport.mock.mockImplementation(async () => Response.json({ taskId: " " }, { status: 201 }));
  await assert.rejects(submitHandoff(request, "fixture-token"), /HANDOFF_UNCONFIRMED/);
});

test("manual handoff keeps an ambiguous request through retry until a receipt confirms it", async (t) => {
  const local = { pairingCredential: "fixture-token" };
  const clicked = t.mock.fn();
  const message = t.mock.fn();
  const sender = { url: "chrome-extension://nimget/popup.html" };
  let posted;
  const notified = new Promise((resolve) => { posted = resolve; });
  globalThis.chrome = {
    runtime: {
      onStartup: { addListener() {} }, onInstalled: { addListener() {} },
      onMessage: { addListener: message }, getURL: (path) => "chrome-extension://nimget/" + path,
    },
    contextMenus: { onClicked: { addListener: clicked } },
    storage: {
      local: {
        getKeys: async () => Object.keys(local),
        setAccessLevel: async () => {},
        get: async (key) => ({ [key]: local[key] }),
        set: async (entries) => Object.assign(local, entries),
        remove: async (key) => { delete local[key]; },
      },
      session: { remove: async () => {} },
    },
    action: { setBadgeBackgroundColor: async () => {}, setBadgeText: async () => {}, setTitle: (notice) => { setImmediate(() => posted(notice)); } },
    i18n: { getMessage: (key) => key },
  };
  t.after(() => { delete globalThis.chrome; });
  const transport = t.mock.method(globalThis, "fetch", async () => { throw new TypeError("reply lost"); });
  await import("../../extension/background.js");
  clicked.mock.calls[0].arguments[0]({ menuItemId: "nimget-send-link", linkUrl: request.url });
  assert.deepEqual(await notified, { title: "handoffUnconfirmed" });

  const key = Object.keys(local).find((name) => name.startsWith("manualHandoff:"));
  const saved = local[key].request;
  assert.equal(saved.url, request.url);
  assert.equal(local[key].feedback, "handoffUnconfirmed");
  const retry = () => new Promise((resolve) => {
    message.mock.calls[0].arguments[0]({ type: "retryManualHandoff", handoffId: saved.handoffId }, sender, resolve);
  });
  transport.mock.mockImplementation(async () => Response.json({ error: { code: "PAIRING_REQUIRED" } }, { status: 403 }));
  await retry();
  assert.equal(local[key].feedback, "handoffPairingRequired");

  transport.mock.mockImplementation(async () => Response.json({ taskId: "saved" }));
  await retry();
  assert.equal(local[key], undefined);
  assert.equal(local.manualHandoffResult, "handoffAccepted");
  for (const call of transport.mock.calls) assert.deepEqual(JSON.parse(call.arguments[1].body), saved);
});
