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
    const r = await api("/api/selectors/preview", { kind: "qualified_name", value: qn });
    if (!r.matches || !r.matches.length) {
      out.textContent = "(no entity matches that qualified_name in code.core)";
      return;
    }
    // Per-source provenance is filled in by T-core (#5). Until then,
    // display the resolution match — the page is correct shape.
    out.textContent = JSON.stringify(
      {
        entity: r.matches[0],
        provenance: "tree-sitter (Phase 1 unifier will surface live LSP + SCIP claims here)",
        resolved_at_kernel_seq: r.resolved_at_kernel_seq,
      },
      null,
      2,
    );
  } catch (e) {
    out.textContent = "error: " + e.message;
  }
});
