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
