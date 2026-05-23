// graph-harness studio — vanilla SPA. Token + Origin-restricted (server side).
// Calls the local daemon HTTP shim under /api/* which proxies to JSON-RPC.

const token = new URLSearchParams(location.search).get("token") || "";
const headers = { "Content-Type": "application/json", "X-Studio-Token": token };

async function api(path, body) {
  const res = await fetch(path, {
    method: body ? "POST" : "GET",
    headers,
    body: body ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) {
    const txt = await res.text();
    throw new Error(`${res.status}: ${txt}`);
  }
  return res.json();
}

// --- nav -------------------------------------------------------------------
document.querySelectorAll("nav button").forEach((b) => {
  b.addEventListener("click", () => {
    document.querySelectorAll("nav button").forEach((x) => x.classList.remove("active"));
    document.querySelectorAll(".page").forEach((p) => p.classList.remove("active"));
    b.classList.add("active");
    document.getElementById("page-" + b.dataset.page).classList.add("active");
  });
});

// --- status banner ---------------------------------------------------------
async function refreshStatus() {
  try {
    const s = await api("/api/status");
    document.getElementById("status").textContent =
      `${s.workspace_root} · seq=${s.last_seq} · ${s.overlay_count} overlay decls`;
  } catch (e) {
    document.getElementById("status").textContent = "offline: " + e.message;
  }
}
refreshStatus();
setInterval(refreshStatus, 5000);

// --- editor + anchor coverage ---------------------------------------------
const editorEl = document.getElementById("editor");
const coverageEl = document.getElementById("coverage");

function analyzeCoverage(src) {
  // Cheap heuristic: each `selector NAME { ... }` block must contain at
  // least one `anchor symbol_fingerprint` line for rename-survival.
  const blocks = [...src.matchAll(/selector\s+([A-Za-z_][A-Za-z0-9_]*)\s*\{([\s\S]*?)\}/g)];
  const out = [];
  for (const [, name, body] of blocks) {
    const anchors = [...body.matchAll(/anchor\s+([a-z_]+)/g)].map((m) => m[1]);
    const hasFp = anchors.includes("symbol_fingerprint") || anchors.includes("body_hash");
    out.push({ name, anchors, hasFp });
  }
  return out;
}

function renderCoverage() {
  const cov = analyzeCoverage(editorEl.value);
  coverageEl.innerHTML = "";
  for (const c of cov) {
    const li = document.createElement("li");
    li.className = c.hasFp ? "ok" : "bad";
    li.textContent = c.hasFp
      ? `${c.name}: ${c.anchors.join(", ")} (rename-safe)`
      : `${c.name}: ${c.anchors.join(", ") || "(none)"} — add a fingerprint or body_hash anchor`;
    coverageEl.appendChild(li);
  }
}
editorEl.addEventListener("input", renderCoverage);
renderCoverage();

document.getElementById("save").addEventListener("click", async () => {
  const rel = document.getElementById("rel-path").value.trim();
  const msg = document.getElementById("save-msg");
  msg.textContent = "saving...";
  try {
    const r = await api("/api/overlay/save", { rel_path: rel, source: editorEl.value });
    msg.textContent = `wrote ${r.bytes_written} bytes → ${r.path.split("/").slice(-2).join("/")}`;
    refreshStatus();
  } catch (e) {
    msg.textContent = "error: " + e.message;
  }
});

// --- selector preview ------------------------------------------------------
document.getElementById("preview-btn").addEventListener("click", async () => {
  const kind = document.getElementById("anchor-kind").value;
  const value = document.getElementById("anchor-value").value;
  const out = document.getElementById("preview-out");
  out.textContent = "resolving...";
  try {
    const r = await api("/api/selectors/preview", { kind, value });
    out.textContent = JSON.stringify(r, null, 2);
  } catch (e) {
    out.textContent = "error: " + e.message;
  }
});

// --- entity drill-down -----------------------------------------------------
document.getElementById("entity-btn").addEventListener("click", async () => {
  const qn = document.getElementById("entity-qn").value;
  const out = document.getElementById("entity-out");
  out.textContent = "looking up...";
  try {
    const r = await api("/api/entity/provenance", { qualified_name: qn });
    const view = r.view;
    if (!view || !view.entity || !view.entity.id) {
      out.textContent = "(no entity matches that qualified_name in code.core)";
      return;
    }
    // SPEC §4.5: surface the folded summary at the top of the
    // drill-down (one-line confidence + freshness + agreeing sources)
    // followed by the per-source claim list verbatim.
    out.textContent = JSON.stringify(
      {
        entity: view.entity,
        provenance_summary: view.provenance?.summary || {},
        sources: view.provenance?.sources || [],
        resolved_at_kernel_seq: r.resolved_at_kernel_seq,
      },
      null,
      2,
    );
  } catch (e) {
    out.textContent = "error: " + e.message;
  }
});

// --- anchor-authoring form -------------------------------------------------
const ANCHOR_HINTS = {
  qualified_name: "fully-qualified Go/TS/Python identifier (e.g. \"pkg.Type.Method\")",
  symbol_fingerprint: "f:Name/sig=Type1,Type2:RetType — survives most renames",
  body_hash: "sha256:... — exact-body fallback",
  path_glob: "src/**/payment_service.* — structural fallback",
  ast_hash: "sha256:... — exact-AST fallback",
};

const anchors = [
  { kind: "qualified_name", value: "CheckoutValidator.Validate" },
  { kind: "symbol_fingerprint", value: "f:Validate/sig=any:error" },
];

function renderAnchors() {
  const wrap = document.getElementById("author-anchors");
  wrap.innerHTML = "";
  anchors.forEach((a, i) => {
    const row = document.createElement("div");
    row.className = "row";
    row.innerHTML = `
      <span class="muted">${a.kind}</span>
      <input value="${a.value.replace(/"/g, "&quot;")}" data-i="${i}" size="60" />
      <button data-rm="${i}">Remove</button>
      <span class="muted">${ANCHOR_HINTS[a.kind] || ""}</span>
    `;
    wrap.appendChild(row);
  });
  for (const inp of wrap.querySelectorAll("input")) {
    inp.addEventListener("input", (e) => {
      anchors[+e.target.dataset.i].value = e.target.value;
      renderGenerated();
    });
  }
  for (const btn of wrap.querySelectorAll("button[data-rm]")) {
    btn.addEventListener("click", (e) => {
      anchors.splice(+e.target.dataset.rm, 1);
      renderAnchors();
      renderGenerated();
    });
  }
}

function renderGenerated() {
  const name = document.getElementById("author-name").value || "Unnamed";
  const unique = document.getElementById("author-unique").checked;
  let gh = `selector ${name} {\n`;
  if (unique) gh += "  unique\n";
  for (const a of anchors) {
    gh += `  anchor ${a.kind} ${JSON.stringify(a.value)}\n`;
  }
  gh += "}\n";
  document.getElementById("author-gh").textContent = gh;
}

document.getElementById("author-add").addEventListener("click", () => {
  const kind = document.getElementById("author-add-kind").value;
  anchors.push({ kind, value: "" });
  renderAnchors();
  renderGenerated();
});
document.getElementById("author-name").addEventListener("input", renderGenerated);
document.getElementById("author-unique").addEventListener("change", renderGenerated);

document.getElementById("author-save").addEventListener("click", async () => {
  const rel = document.getElementById("author-rel").value.trim();
  const msg = document.getElementById("author-msg");
  msg.textContent = "saving...";
  try {
    const r = await api("/api/overlay/save", {
      rel_path: rel,
      source: document.getElementById("author-gh").textContent,
    });
    msg.textContent = `wrote ${r.bytes_written} bytes → ${r.path.split("/").slice(-2).join("/")}`;
    refreshStatus();
  } catch (e) {
    msg.textContent = "error: " + e.message;
  }
});

renderAnchors();
renderGenerated();

// --- framework-entity browsers (P2.T38) -----------------------------------
//
// Three tabs (Routes / Events / Schemas) each render a paginated table
// of entities pulled from `framework.*` JSON-RPC handlers (via the
// /api/framework/* HTTP shims). The "show flow steps that touch this
// entity" drill-down lists every flow step whose selector resolves to
// the clicked row via the Pass-0.5-A reverse selector index.
//
// Text-only visualization — no graph drawing in v1 per plan §2.

function entityMethod(e) {
  // For Route entities the HTTP method lives on kind_tag.
  return e.kind_tag || "";
}

function entityHandler(e) {
  return e.qualified_name || "";
}

async function loadStepsTouching(entityID, sink) {
  if (!entityID) {
    sink.textContent = "(no entity selected)";
    return;
  }
  sink.textContent = "loading...";
  try {
    const r = await api("/api/framework/steps_touching", { entity_id: entityID });
    if (!r.steps || r.steps.length === 0) {
      sink.textContent =
        `(no flow steps bind to entity ${entityID.slice(0, 12)}…)\n` +
        `— either no selector mentions this entity yet, or the resolver hasn't run.`;
      return;
    }
    const lines = r.steps.map(
      (s) =>
        `selector=${s.selector_id}` +
        (s.flow_id ? `  flow=${s.flow_id}` : "") +
        `  via=${s.via_anchor}  bound@seq=${s.bound_at_seq}`,
    );
    sink.textContent = `entity_id: ${r.entity_id}\n\n${lines.join("\n")}`;
  } catch (e) {
    sink.textContent = "error: " + e.message;
  }
}

function rowSelectHandler(tableEl, onSelect) {
  return (ev) => {
    const tr = ev.target.closest("tr");
    if (!tr || !tr.dataset.entityId) return;
    for (const r of tableEl.querySelectorAll("tbody tr")) r.classList.remove("selected");
    tr.classList.add("selected");
    onSelect(tr.dataset.entityId);
  };
}

function escapeHTML(s) {
  return String(s || "")
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

// --- Routes tab -----------------------------------------------------------
async function loadRoutes() {
  const fw = document.getElementById("routes-framework").value.trim();
  const msg = document.getElementById("routes-msg");
  const tbody = document.querySelector("#routes-table tbody");
  msg.textContent = "loading...";
  tbody.innerHTML = "";
  try {
    const r = await api("/api/framework/routes", { framework: fw });
    msg.textContent = `${r.items.length} shown (total ${r.total})`;
    for (const e of r.items) {
      const tr = document.createElement("tr");
      tr.dataset.entityId = e.id;
      tr.innerHTML =
        `<td class="method">${escapeHTML(entityMethod(e))}</td>` +
        `<td>${escapeHTML(e.qualified_name)}</td>` +
        `<td class="muted">${escapeHTML(entityHandler(e))}</td>` +
        `<td class="muted">${escapeHTML(e.kind_tag)}</td>` +
        `<td><button data-steps="${escapeHTML(e.id)}">View steps</button></td>`;
      tbody.appendChild(tr);
    }
  } catch (e) {
    msg.textContent = "error: " + e.message;
  }
}
document.getElementById("routes-refresh").addEventListener("click", loadRoutes);
document
  .querySelector("#routes-table")
  .addEventListener(
    "click",
    rowSelectHandler(document.getElementById("routes-table"), (id) =>
      loadStepsTouching(id, document.getElementById("routes-steps")),
    ),
  );

// --- Events tab -----------------------------------------------------------
async function loadEventsTable() {
  const fw = document.getElementById("events-framework").value.trim();
  const msg = document.getElementById("events-msg");
  const tbody = document.querySelector("#events-table tbody");
  msg.textContent = "loading...";
  tbody.innerHTML = "";
  try {
    const r = await api("/api/framework/events", { framework: fw });
    msg.textContent = `${r.items.length} shown (total ${r.total})`;
    for (const e of r.items) {
      const tr = document.createElement("tr");
      tr.dataset.entityId = e.id;
      tr.dataset.eventName = e.qualified_name || "";
      tr.dataset.transport = (e.kind_tag || "").replace(/^topic:/, "");
      tr.innerHTML =
        `<td>${escapeHTML(e.qualified_name)}</td>` +
        `<td class="muted">${escapeHTML(e.kind_tag)}</td>` +
        `<td class="muted">${escapeHTML(e.path)}</td>` +
        `<td><button data-steps="${escapeHTML(e.id)}">View steps</button></td>`;
      tbody.appendChild(tr);
    }
  } catch (e) {
    msg.textContent = "error: " + e.message;
  }
}
document.getElementById("events-refresh").addEventListener("click", loadEventsTable);
document.getElementById("events-table").addEventListener(
  "click",
  rowSelectHandler(document.getElementById("events-table"), async (id) => {
    const tr = document.querySelector(`#events-table tbody tr[data-entity-id="${CSS.escape(id)}"]`);
    const transport = tr ? tr.dataset.transport : "";
    const sinkPubSub = document.getElementById("events-pubsub");
    const sinkSteps = document.getElementById("events-steps");
    // Walk the publisher + subscriber tables filtered by transport so
    // the drill-down stays scoped to the selected event's transport.
    sinkPubSub.textContent = "loading publishers/subscribers...";
    try {
      const pubs = await api("/api/framework/event_publishers", { framework: transport });
      const subs = await api("/api/framework/event_subscribers", { framework: transport });
      const eventName = tr ? tr.dataset.eventName : "";
      const matching = (list) =>
        list.items.filter((x) => !eventName || x.qualified_name === eventName);
      const pubLines = matching(pubs).map(
        (e) => `  pub: ${e.qualified_name}  [${e.kind_tag}]  ${e.path}`,
      );
      const subLines = matching(subs).map(
        (e) => `  sub: ${e.qualified_name}  [${e.kind_tag}]  ${e.path}`,
      );
      sinkPubSub.textContent =
        `transport=${transport || "(any)"}  event=${eventName}\n\n` +
        (pubLines.length ? pubLines.join("\n") : "  (no publishers)") +
        "\n" +
        (subLines.length ? subLines.join("\n") : "  (no subscribers)");
    } catch (e) {
      sinkPubSub.textContent = "error: " + e.message;
    }
    loadStepsTouching(id, sinkSteps);
  }),
);

// --- Schemas tab ----------------------------------------------------------
async function loadSchemas() {
  const fw = document.getElementById("schemas-framework").value.trim();
  const msg = document.getElementById("schemas-msg");
  const tbody = document.querySelector("#schemas-table tbody");
  msg.textContent = "loading...";
  tbody.innerHTML = "";
  try {
    const r = await api("/api/framework/schemas", { framework: fw });
    msg.textContent = `${r.items.length} shown (total ${r.total})`;
    for (const e of r.items) {
      const tr = document.createElement("tr");
      tr.dataset.entityId = e.id;
      tr.dataset.table = e.qualified_name || "";
      tr.innerHTML =
        `<td>${escapeHTML(e.qualified_name)}</td>` +
        `<td class="muted">${escapeHTML(e.kind_tag)}</td>` +
        `<td class="muted">${escapeHTML(e.path)}</td>` +
        `<td><button data-steps="${escapeHTML(e.id)}">View steps</button></td>`;
      tbody.appendChild(tr);
    }
  } catch (e) {
    msg.textContent = "error: " + e.message;
  }
}
document.getElementById("schemas-refresh").addEventListener("click", loadSchemas);
document.getElementById("schemas-table").addEventListener(
  "click",
  rowSelectHandler(document.getElementById("schemas-table"), async (id) => {
    const tr = document.querySelector(`#schemas-table tbody tr[data-entity-id="${CSS.escape(id)}"]`);
    const table = tr ? tr.dataset.table : "";
    const fieldsBody = document.querySelector("#schema-fields-table tbody");
    fieldsBody.innerHTML = "<tr><td colspan='3' class='muted'>loading...</td></tr>";
    try {
      // SchemaField qualified_name is "<table>.<field>"; the
      // framework param on schema_fields is interpreted as a table-
      // name prefix filter (see filterByTablePrefix in the daemon).
      const r = await api("/api/framework/schema_fields", { framework: table });
      fieldsBody.innerHTML = "";
      if (r.items.length === 0) {
        fieldsBody.innerHTML = "<tr><td colspan='3' class='muted'>(no fields)</td></tr>";
      }
      for (const f of r.items) {
        const fieldName = (f.qualified_name || "").split(".").slice(1).join(".") || f.qualified_name;
        const row = document.createElement("tr");
        row.innerHTML =
          `<td>${escapeHTML(fieldName)}</td>` +
          `<td class="muted">${escapeHTML(f.kind_tag)}</td>` +
          `<td class="muted">${escapeHTML(f.path)}</td>`;
        fieldsBody.appendChild(row);
      }
    } catch (e) {
      fieldsBody.innerHTML = `<tr><td colspan='3'>error: ${escapeHTML(e.message)}</td></tr>`;
    }
    loadStepsTouching(id, document.getElementById("schemas-steps"));
  }),
);

// Lazy-load each browser the first time its tab activates.
const lazyLoaders = {
  routes: { fn: loadRoutes, loaded: false },
  events: { fn: loadEventsTable, loaded: false },
  schemas: { fn: loadSchemas, loaded: false },
};
document.querySelectorAll("nav button").forEach((b) => {
  b.addEventListener("click", () => {
    const tab = b.dataset.page;
    const loader = lazyLoaders[tab];
    if (loader && !loader.loaded) {
      loader.loaded = true;
      loader.fn();
    }
  });
});
