// The page: an add-listing form, weight sliders, the ranked table, the
// visitor's places, and a map. Everything it shows comes from the JSON API;
// scores are computed by the server, never here.
//
// Text from visitors (addresses, place names) is only ever set with
// textContent, never as HTML.

const POLL_MS = 1500;
const WEIGHTS_KEY = "apartment-finder.weights";

const state = {
  region: null,
  criteria: [], // from the last listings response, in order
  listings: [],
  places: [],
  weights: loadWeights(), // criterion key -> weight, only where changed
  selected: null, // listing id
  follow: null, // a just-added listing id: show it on the map once located
  followPlace: null, // the same for a just-added place
  pollTimer: null,
  refreshing: null,
  rendered: "", // what the last render showed, to skip identical ones
};

let map, listingLayer, placeLayer;
const markers = new Map(); // listing id -> marker

// --- API -------------------------------------------------------------------

class ApiError extends Error {
  constructor(status, body) {
    super(body?.error || `The server answered ${status}.`);
    this.status = status;
    this.fields = body?.fields || {};
  }
}

async function api(method, path, body) {
  const options = { method, headers: {} };
  if (body !== undefined) {
    options.headers["Content-Type"] = "application/json";
    options.body = JSON.stringify(body);
  }
  let response;
  try {
    response = await fetch(path, options);
  } catch {
    throw new ApiError(0, { error: "Couldn't reach the server. Check your connection and try again." });
  }
  if (response.status === 204) return null;
  const data = await response.json().catch(() => null);
  if (!response.ok) throw new ApiError(response.status, data);
  return data;
}

// --- Weights ---------------------------------------------------------------

function loadWeights() {
  try {
    const stored = JSON.parse(localStorage.getItem(WEIGHTS_KEY));
    return stored && typeof stored === "object" ? stored : {};
  } catch {
    return {};
  }
}

function saveWeights() {
  try {
    localStorage.setItem(WEIGHTS_KEY, JSON.stringify(state.weights));
  } catch {
    // Storage can be off, e.g. in a private window; weights then last
    // until the page is closed.
  }
}

// weightQuery returns the query string for the changed weights. Only
// criteria the last response listed are sent: a saved weight can belong to a
// place deleted since, and the server rejects unknown criteria.
function weightQuery() {
  const known = new Set(state.criteria.map((c) => c.key));
  const params = new URLSearchParams();
  for (const [key, weight] of Object.entries(state.weights)) {
    if (known.has(key)) params.set(`weight.${key}`, String(weight));
  }
  const query = params.toString();
  return query ? `?${query}` : "";
}

// --- Loading ---------------------------------------------------------------

async function refresh() {
  // One refresh at a time; a request made meanwhile waits for it.
  if (state.refreshing) return state.refreshing;
  state.refreshing = (async () => {
    try {
      const [listings, places] = await Promise.all([
        api("GET", `/api/listings${weightQuery()}`),
        api("GET", "/api/places"),
      ]);
      showWeightsError("");
      state.criteria = listings.criteria;
      state.listings = listings.listings;
      state.places = places.places;
      // Polling mostly brings back the same data; re-rendering it anyway
      // would reset things like a half-clicked delete button.
      const snapshot = JSON.stringify([state.criteria, state.listings, state.places, state.selected]);
      if (snapshot !== state.rendered) {
        state.rendered = snapshot;
        render();
      }
    } catch (err) {
      if (err.status === 400) {
        // Weights the server refused, e.g. all of them at 0: keep the last
        // ranking and say why.
        showWeightsError(err.message);
      } else {
        showBanner(err.message);
      }
    } finally {
      state.refreshing = null;
    }
    schedulePoll();
  })();
  return state.refreshing;
}

// schedulePoll refreshes again shortly while anything is still being
// measured, so new listings turn from pending to scored without a reload.
function schedulePoll() {
  clearTimeout(state.pollTimer);
  const pending = state.listings.some((l) => l.status === "pending") ||
    state.places.some((p) => p.status === "pending") ||
    state.listings.some((l) => l.score?.breakdown.some((c) => c.outcome === "pending"));
  if (pending) state.pollTimer = setTimeout(refresh, POLL_MS);
}

// --- Rendering -------------------------------------------------------------

function render() {
  renderSliders();
  renderTable();
  renderMarkers();
  renderPlaces();
  // A just-added listing may be anywhere in the region, off the map's
  // current view; once it's located, bring it into view.
  const followed = state.listings.find((l) => l.id === state.follow);
  if (followed && followed.status !== "pending") {
    state.follow = null;
    if (followed.location) {
      map.flyTo([followed.location.lat, followed.location.lon], Math.max(map.getZoom(), 14), { duration: 0.6 });
      markers.get(followed.id)?.openPopup();
    }
  }
  const place = state.places.find((p) => p.id === state.followPlace);
  if (place && place.status !== "pending") {
    state.followPlace = null;
    if (place.location) map.panInside([place.location.lat, place.location.lon], { padding: [56, 56] });
  }
}

// fitAll shows every located listing and place, or the region's default
// view when there are none yet.
function fitAll() {
  const points = [...state.listings, ...state.places].filter((x) => x.location).map((x) => [x.location.lat, x.location.lon]);
  if (points.length > 0) map.fitBounds(points, { padding: [40, 40], maxZoom: 15 });
}

function el(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [key, value] of Object.entries(attrs)) {
    if (value === undefined || value === null || value === false) continue;
    if (key === "class") node.className = value;
    else if (key === "text") node.textContent = value;
    else if (key.startsWith("on")) node.addEventListener(key.slice(2), value);
    else node.setAttribute(key, value === true ? "" : value);
  }
  for (const child of children) {
    if (child !== null && child !== undefined && child !== false) node.append(child);
  }
  return node;
}

const number = new Intl.NumberFormat("en", { maximumFractionDigits: 1 });
const money = new Intl.NumberFormat("en", { maximumFractionDigits: 2 });

// Numbers and their units are joined with no-break spaces, so a line never
// ends between "12.34" and "€/m²".
function rooms(n) {
  return `${number.format(n)}\u00a0${n === 1 ? "room" : "rooms"}`;
}

function scoreClass(total) {
  return `s${Math.min(10, Math.max(0, Math.round(total / 10)))}`;
}

function criterion(key) {
  return state.criteria.find((c) => c.key === key);
}

// renderSliders rebuilds the sliders only when the criteria change, so a
// slider being dragged keeps its focus.
function renderSliders() {
  const container = document.getElementById("sliders");
  const keys = state.criteria.map((c) => c.key).join("|");
  if (container.dataset.keys !== keys) {
    container.dataset.keys = keys;
    container.replaceChildren();
    let group = null;
    for (const c of state.criteria) {
      const kind = c.key.startsWith("place:") ? "Your places" : c.key.startsWith("category:") ? "Everyday places" : "The apartment";
      if (kind !== group) {
        group = kind;
        container.append(el("p", { class: "slider-group", text: kind }));
      }
      const id = `weight-${c.key.replace(/[^a-z0-9_-]/gi, "-")}`;
      const output = el("output", { for: id, "data-key": c.key });
      const input = el("input", {
        id, type: "range", min: "0", max: "10", step: "1", "data-key": c.key,
        oninput: (event) => onSlider(c.key, Number(event.target.value), output),
      });
      container.append(el("div", { class: "slider" }, el("label", { for: id, text: c.label }), output, input));
    }
  }
  for (const c of state.criteria) {
    const input = container.querySelector(`input[data-key="${CSS.escape(c.key)}"]`);
    const output = container.querySelector(`output[data-key="${CSS.escape(c.key)}"]`);
    if (document.activeElement !== input) input.value = String(c.weight);
    output.textContent = String(c.weight);
  }
}

let sliderTimer = null;
function onSlider(key, weight, output) {
  output.textContent = String(weight);
  const c = criterion(key);
  if (c && weight === c.default_weight) delete state.weights[key];
  else state.weights[key] = weight;
  saveWeights();
  clearTimeout(sliderTimer);
  sliderTimer = setTimeout(refresh, 120);
}

function renderTable() {
  const body = document.getElementById("listings");
  body.replaceChildren();
  if (state.listings.length === 0) {
    body.append(el("tr", {}, el("td", { colspan: "3", class: "empty", text: "No listings yet. Add one above." })));
    return;
  }
  let rank = 0;
  for (const l of state.listings) {
    const scored = l.score !== null;
    if (scored) rank++;
    const selected = l.id === state.selected;
    const row = el("tr", { class: `listing${selected ? " selected" : ""}`, "data-id": String(l.id), onclick: () => select(l.id, true) },
      el("td", { class: "rank", text: scored ? String(rank) : "" }),
      el("td", { class: "score-cell" }, scoreBadge(l)),
      el("td", {}, listingSummary(l)),
    );
    body.append(row);
    if (selected && scored) {
      body.append(el("tr", { class: "details" }, el("td", {}), el("td", { colspan: "2" }, breakdown(l))));
    }
  }
}

function scoreBadge(l) {
  if (l.score) {
    return el("span", { class: `badge ${scoreClass(l.score.total)}`, title: "Score out of 100", text: String(Math.round(l.score.total)) });
  }
  switch (l.status) {
    case "pending":
      return el("span", { class: "status pending", text: "Scoring" });
    case "not_found":
      return el("span", { class: "status problem", text: "Not found" });
    case "failed":
      return el("span", { class: "status problem", text: "Failed" });
    default:
      return el("span", { class: "badge unscored", title: "Nothing with weight was measured", text: "–" });
  }
}

function listingSummary(l) {
  const perM2 = l.warm_rent_eur / l.size_m2;
  const fragment = document.createDocumentFragment();
  fragment.append(
    el("button", { class: "listing-title", type: "button", "aria-expanded": String(l.id === state.selected) },
      el("span", { text: l.address }), l.example ? el("span", { class: "tag", text: "Example" }) : null),
    el("p", { class: "meta", text: `${money.format(l.warm_rent_eur)}\u00a0€ warm · ${number.format(l.size_m2)}\u00a0m² · ${rooms(l.rooms)} · ${money.format(perM2)}\u00a0€/m²` }),
  );
  // Where the address was matched, so a wrong match is visible. The
  // examples are ours, so they skip it.
  if (!l.example && l.matched_address && l.matched_address !== l.address) {
    const precision = l.precision === "street" ? " (street only)" : "";
    fragment.append(el("p", { class: "matched", text: `Matched to ${l.matched_address}${precision}` }));
  }
  if (l.status_detail) fragment.append(el("p", { class: "detail", text: l.status_detail }));
  const actions = el("div", { class: "row-actions" });
  if (l.source_url) {
    actions.append(el("a", { href: l.source_url, target: "_blank", rel: "noopener noreferrer nofollow", onclick: (e) => e.stopPropagation(), text: "Advert" }));
  }
  if (!l.example) actions.append(deleteButton(() => remove(`/api/listings/${l.id}`), `Delete ${l.address}`));
  if (actions.childElementCount > 0) fragment.append(actions);
  return fragment;
}

// deleteButton asks for a second click instead of a dialog.
function deleteButton(onConfirm, label) {
  let armed = null;
  const button = el("button", {
    class: "delete", type: "button", text: "Delete", "aria-label": label,
    onclick: (event) => {
      event.stopPropagation();
      if (armed) {
        clearTimeout(armed);
        button.disabled = true;
        onConfirm();
        return;
      }
      button.classList.add("confirm");
      button.textContent = "Delete?";
      armed = setTimeout(() => {
        armed = null;
        button.classList.remove("confirm");
        button.textContent = "Delete";
      }, 3000);
    },
  });
  return button;
}

function breakdown(l) {
  const list = el("ul", { class: "breakdown", "aria-label": "Score breakdown" });
  for (const part of l.score.breakdown) {
    const c = criterion(part.key);
    if (!c) continue;
    const bar = el("span", { class: scoreClass(part.score * 100) });
    bar.style.width = `${Math.round(part.score * 100)}%`;
    list.append(el("li", { class: c.weight === 0 ? "off" : null },
      el("span", { class: "what", text: c.label }),
      el("span", { class: "points", text: c.weight === 0 ? "not counted" : `${number.format(part.points)} pts` }),
      el("span", { class: "value", text: describe(part, c) }),
      el("span", { class: "bar", "aria-hidden": "true" }, bar),
    ));
  }
  return list;
}

// describe says in words what was measured for one criterion.
function describe(part, c) {
  switch (part.outcome) {
    case "measured": {
      const value = c.unit === "€/m²" ? `${money.format(part.value)}\u00a0€/m²`
        : c.key === "rooms" ? rooms(part.value)
        : `${number.format(part.value)}\u00a0${c.unit}`;
      const place = part.place ? ` to ${part.place}` : "";
      const distance = part.distance_m ? ` (${part.distance_m >= 1000 ? `${number.format(part.distance_m / 1000)}\u00a0km` : `${Math.round(part.distance_m)}\u00a0m`})` : "";
      return `${value}${place}${distance}`;
    }
    case "none_nearby":
      return `None within ${number.format(c.worst)}\u00a0${c.unit}`;
    case "unreachable":
      return "No route found, so it's left out";
    default:
      return "Still being measured";
  }
}

function renderMarkers() {
  const seen = new Set();
  for (const l of state.listings) {
    if (!l.location) continue;
    seen.add(l.id);
    const label = l.score ? String(Math.round(l.score.total)) : "–";
    const classes = ["pin", l.score ? scoreClass(l.score.total) : "unscored", l.example ? "example" : "", l.id === state.selected ? "selected" : ""].join(" ").trim();
    const icon = L.divIcon({ className: classes, html: `<span>${label}</span>`, iconSize: [30, 30], iconAnchor: [15, 15], popupAnchor: [0, -14] });
    let marker = markers.get(l.id);
    if (!marker) {
      marker = L.marker([l.location.lat, l.location.lon], { icon, title: l.address, keyboard: true, riseOnHover: true })
        .on("click", () => select(l.id, false))
        .addTo(listingLayer);
      markers.set(l.id, marker);
    } else {
      marker.options.title = l.address;
      marker.setLatLng([l.location.lat, l.location.lon]).setIcon(icon);
    }
    marker.setZIndexOffset(l.id === state.selected ? 1000 : Math.round(l.score?.total ?? 0));
    marker.bindPopup(popup(l), { closeButton: false, autoPanPaddingTopLeft: [16, 16] });
  }
  for (const [id, marker] of markers) {
    if (!seen.has(id)) {
      marker.remove();
      markers.delete(id);
    }
  }
}

function popup(l) {
  return el("div", { class: "popup" },
    el("strong", { text: l.address }),
    el("span", { class: "meta", text: l.score ? `Score ${Math.round(l.score.total)} · ${money.format(l.warm_rent_eur)}\u00a0€ warm · ${number.format(l.size_m2)}\u00a0m²` : "Not scored" }),
  );
}

function renderPlaces() {
  const list = document.getElementById("places");
  list.replaceChildren();
  placeLayer.clearLayers();
  for (const p of state.places) {
    const how = p.mode === "bike" ? "by bike" : "on foot";
    let sub = `${p.matched_address || p.address} · ${how}`;
    if (p.status === "pending") sub = `Finding ${p.address}…`;
    if (p.status === "not_found" || p.status === "failed") sub = p.status_detail || "Couldn't be found.";
    list.append(el("li", {},
      el("span", { class: "name", text: p.name }),
      el("span", { class: `sub${p.status === "not_found" || p.status === "failed" ? " detail" : ""}`, text: sub }),
      deleteButton(() => {
        // Its weight goes with it, so the next request doesn't name a
        // criterion that no longer exists.
        delete state.weights[p.criterion];
        saveWeights();
        state.criteria = state.criteria.filter((c) => c.key !== p.criterion);
        remove(`/api/places/${p.id}`);
      }, `Delete ${p.name}`),
    ));
    if (p.location) {
      const icon = L.divIcon({ className: "place-pin", html: "<span></span>", iconSize: [18, 18], iconAnchor: [9, 9] });
      L.marker([p.location.lat, p.location.lon], { icon, keyboard: false, interactive: false })
        .bindTooltip(el("span", { text: p.name }), { permanent: true, direction: "top", offset: [0, -10], className: "place-label" })
        .addTo(placeLayer);
    }
  }
}

// --- Interaction -----------------------------------------------------------

function select(id, fromTable) {
  // Re-rendering replaces the row; keep keyboard focus on it.
  const hadFocus = Boolean(document.activeElement?.closest("tr.listing"));
  state.selected = state.selected === id && fromTable ? null : id;
  state.rendered = "";
  renderTable();
  renderMarkers();
  if (hadFocus) document.querySelector(`tr.listing[data-id="${id}"] .listing-title`)?.focus();
  const l = state.listings.find((x) => x.id === id);
  if (!l || state.selected === null) return;
  if (l.location) {
    const marker = markers.get(id);
    if (fromTable) map.flyTo([l.location.lat, l.location.lon], Math.max(map.getZoom(), 15), { duration: 0.6 });
    marker?.openPopup();
  }
  if (!fromTable) {
    document.querySelector(`tr.listing[data-id="${id}"]`)?.scrollIntoView({ block: "nearest", behavior: "smooth" });
  }
}

async function remove(path) {
  try {
    await api("DELETE", path);
  } catch (err) {
    showBanner(err.message);
  }
  await refresh();
}

function showBanner(message) {
  const banner = document.getElementById("banner");
  banner.textContent = message;
  banner.hidden = !message;
}

function showWeightsError(message) {
  document.getElementById("weights-error").textContent = message;
}

function announce(message) {
  document.getElementById("status").textContent = message;
}

// submitForm posts a form as JSON and marks each field the server rejects.
async function submitForm(form, path, prefix, collect) {
  const button = form.querySelector("button[type=submit]");
  for (const input of form.querySelectorAll("input, select")) {
    input.removeAttribute("aria-invalid");
    document.getElementById(`${input.id}-error`).textContent = "";
  }
  showBanner("");
  button.disabled = true;
  try {
    const created = await api("POST", path, collect(new FormData(form)));
    form.reset();
    return created;
  } catch (err) {
    const fields = Object.entries(err.fields || {});
    for (const [name, message] of fields) {
      const input = document.getElementById(`${prefix}-${name}`);
      const error = document.getElementById(`${prefix}-${name}-error`);
      if (input && error) {
        input.setAttribute("aria-invalid", "true");
        error.textContent = message;
      }
    }
    if (fields.length > 0) form.querySelector("[aria-invalid=true]")?.focus();
    else showBanner(err.message);
    return null;
  } finally {
    button.disabled = false;
  }
}

function numberOrNull(value) {
  const text = String(value ?? "").trim().replace(",", ".");
  return text === "" ? null : Number(text);
}

async function onAddListing(event) {
  event.preventDefault();
  const created = await submitForm(event.target, "/api/listings", "listing", (data) => {
    const body = {
      address: data.get("address"),
      warm_rent_eur: numberOrNull(data.get("warm_rent_eur")),
      size_m2: numberOrNull(data.get("size_m2")),
      rooms: numberOrNull(data.get("rooms")),
    };
    if (String(data.get("source_url")).trim()) body.source_url = data.get("source_url");
    return body;
  });
  if (created) {
    state.selected = created.id;
    state.follow = created.id;
    announce("Listing added. Scoring it now.");
    await refresh();
    document.querySelector(`tr.listing[data-id="${created.id}"]`)?.scrollIntoView({ block: "nearest", behavior: "smooth" });
  }
}

async function onAddPlace(event) {
  event.preventDefault();
  const created = await submitForm(event.target, "/api/places", "place", (data) => ({
    name: data.get("name"),
    address: data.get("address"),
    mode: data.get("mode"),
  }));
  if (created) {
    state.followPlace = created.id;
    announce(`${created.name} added. Measuring trips to it now.`);
    await refresh();
  }
}

// --- Start -----------------------------------------------------------------

async function start() {
  try {
    state.region = await api("GET", "/api/region");
  } catch (err) {
    showBanner(err.message);
    return;
  }
  document.getElementById("region-name").textContent = state.region.name;

  map = L.map("map", { zoomControl: true }).setView([state.region.map.lat, state.region.map.lon], state.region.map.zoom);
  L.tileLayer("https://tile.openstreetmap.org/{z}/{x}/{y}.png", {
    maxZoom: 19,
    attribution: '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors',
  }).addTo(map);
  // The covered area: addresses outside it can't be scored.
  L.polygon(state.region.outline.map((ring) => ring.map(([lon, lat]) => [lat, lon])), {
    color: "#2f5bd3", weight: 2, opacity: 0.6, fill: false, dashArray: "6 6", interactive: false,
  }).addTo(map);
  placeLayer = L.layerGroup().addTo(map);
  listingLayer = L.layerGroup().addTo(map);

  // The weights start open on wide screens, where there's room.
  document.getElementById("weights").open = window.matchMedia("(min-width: 900px)").matches;

  document.getElementById("add-listing").addEventListener("submit", onAddListing);
  document.getElementById("add-place").addEventListener("submit", onAddPlace);
  document.getElementById("reset-weights").addEventListener("click", () => {
    state.weights = {};
    saveWeights();
    refresh();
  });

  await refresh();
  // Saved weights can only be checked against the criteria the first
  // response listed, so they apply from the second request on.
  if (state.criteria.some((c) => c.key in state.weights)) await refresh();
  fitAll();
}

start();
