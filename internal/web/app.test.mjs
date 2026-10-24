// Tests of the page script's loading logic, run with `node --test`. The
// script runs in a sandbox with a fake fetch and only the elements loading
// touches; rendering is replaced, so no browser is needed.

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";

const source = readFileSync(new URL("static/app.js", import.meta.url), "utf8");

// page loads the script. Each request waits until the test answers it with
// respond(body) or fail(), so tests control the order responses arrive in.
function page() {
  const requests = [];
  const elements = {
    banner: { textContent: "", hidden: true },
    "weights-error": { textContent: "" },
  };
  const sandbox = {
    URLSearchParams, setTimeout, clearTimeout,
    document: { getElementById: (id) => elements[id] },
    fetch: (path) => {
      // The region request is left unanswered: it would build the map.
      if (path === "/api/region") return new Promise(() => {});
      return new Promise((resolve, reject) => {
        requests.push({
          path,
          respond: (body) => resolve({ status: 200, ok: true, json: async () => body }),
          fail: () => reject(new TypeError("Failed to fetch")),
        });
      });
    },
  };
  vm.createContext(sandbox);
  vm.runInContext(source, sandbox);
  const state = vm.runInContext("state", sandbox);
  const rendered = [];
  sandbox.render = () => rendered.push(JSON.parse(JSON.stringify(state.criteria)));
  return {
    requests, elements, rendered,
    state,
    refresh: () => sandbox.refresh(),
    showBanner: (message) => sandbox.showBanner(message),
  };
}

// settle lets every response that has arrived be handled.
const settle = () => new Promise((resolve) => setImmediate(resolve));

// answer answers the open requests like the server: the listings with the
// rent weight from the query string.
async function answer(p) {
  await settle();
  const open = p.requests.splice(0);
  for (const r of open) {
    if (r.path.startsWith("/api/listings")) {
      const weight = new URL(r.path, "http://x").searchParams.get("weight.rent_per_m2");
      r.respond({ criteria: [rent(weight === null ? 3 : Number(weight))], listings: [] });
    } else {
      r.respond({ places: [] });
    }
  }
  await settle();
  return open;
}

function rent(weight) {
  return { key: "rent_per_m2", label: "Rent", default_weight: 3, weight };
}

test("the banner goes once loading works again", async () => {
  const p = page();
  const failed = p.refresh();
  await settle();
  for (const r of p.requests.splice(0)) r.fail();
  await failed;
  assert.equal(p.elements.banner.hidden, false);
  assert.match(p.elements.banner.textContent, /Couldn't reach the server/);

  const loaded = p.refresh();
  await answer(p);
  await loaded;
  assert.equal(p.elements.banner.hidden, true);
  assert.equal(p.elements.banner.textContent, "");
});

test("a banner about a failed action stays when loading works", async () => {
  const p = page();
  p.showBanner("You can add at most 10 places.");
  const loaded = p.refresh();
  await answer(p);
  await loaded;
  assert.equal(p.elements.banner.hidden, false);
  assert.equal(p.elements.banner.textContent, "You can add at most 10 places.");
});
