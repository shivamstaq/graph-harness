// graph-harness VS Code extension. Talks to the workspace daemon by
// shelling out to the `graph-harness` CLI on $PATH; the CLI proxies to the
// daemon over the workspace socket. There is no second protocol.
//
// Activation: .gh files (always), and any .go / .ts / .tsx / .js / .jsx /
// .py file once a `.graph-harness` directory is found above it. The
// language-aware activation lets the extension surface findings on the
// language the developer is editing without spinning up for unrelated
// projects (P1.T37).

import * as vscode from "vscode";
import { exec } from "child_process";
import { promisify } from "util";
import * as crypto from "crypto";

import { JsonRpcClient, unixSocketConnector } from "./rpc/client";
import { discoverWorkspaceRoot, socketPath } from "./rpc/socketPath";
import { FrameworkStore } from "./framework/store";
import { FrameworkLensProvider } from "./framework/lensProvider";
import { DaemonBridge } from "./framework/daemonBridge";

const execAsync = promisify(exec);

const SELECTOR_RE = /^\s*selector\s+([A-Za-z_][A-Za-z0-9_]*)\s*\{/;

// --- CLI bridge ------------------------------------------------------------

interface CLIOptions {
  cliPath: string;
  cwd: string;
}

function getOptions(doc?: vscode.TextDocument): CLIOptions {
  const cfg = vscode.workspace.getConfiguration("graphHarness");
  const cliPath = cfg.get<string>("cliPath", "graph-harness");
  const cwd = doc?.uri.fsPath
    ? vscode.workspace.getWorkspaceFolder(doc.uri)?.uri.fsPath
    : vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  return { cliPath, cwd: cwd || process.cwd() };
}

async function runCLI(args: string[], opts: CLIOptions): Promise<string> {
  const cmd = [opts.cliPath, ...args.map((a) => `'${a.replace(/'/g, "'\\''")}'`)].join(" ");
  const { stdout } = await execAsync(cmd, { cwd: opts.cwd, maxBuffer: 16 * 1024 * 1024 });
  return stdout;
}

async function selectorTest(name: string, opts: CLIOptions): Promise<{ matches: number } | null> {
  try {
    const out = await runCLI(["selectors", "test", name, "--json"], opts);
    const env = JSON.parse(out);
    return { matches: Array.isArray(env.matches) ? env.matches.length : 0 };
  } catch {
    return null;
  }
}

async function validateDiff(diff: string, opts: CLIOptions): Promise<any> {
  const cmd = `${opts.cliPath} validate-diff --json --diff -`;
  return new Promise((resolve, reject) => {
    const child = exec(cmd, { cwd: opts.cwd, maxBuffer: 16 * 1024 * 1024 }, (err, stdout) => {
      if (err) reject(err);
      else {
        try {
          resolve(JSON.parse(stdout));
        } catch (e) {
          reject(e);
        }
      }
    });
    child.stdin?.write(diff);
    child.stdin?.end();
  });
}

// --- Code lens provider ----------------------------------------------------

class GHCodeLensProvider implements vscode.CodeLensProvider {
  private _onDidChange = new vscode.EventEmitter<void>();
  readonly onDidChangeCodeLenses = this._onDidChange.event;

  refresh() {
    this._onDidChange.fire();
  }

  async provideCodeLenses(doc: vscode.TextDocument): Promise<vscode.CodeLens[]> {
    const cfg = vscode.workspace.getConfiguration("graphHarness");
    if (!cfg.get<boolean>("codeLens.enabled", true)) {
      return [];
    }
    const lenses: vscode.CodeLens[] = [];
    const opts = getOptions(doc);
    for (let i = 0; i < doc.lineCount; i++) {
      const line = doc.lineAt(i).text;
      const m = SELECTOR_RE.exec(line);
      if (!m) continue;
      const range = new vscode.Range(i, 0, i, line.length);
      const name = m[1];
      const result = await selectorTest(name, opts);
      const title =
        result === null
          ? `$(graph-harness) ${name}: daemon unavailable`
          : `$(graph-harness) ${name}: ${result.matches} match${result.matches === 1 ? "" : "es"}`;
      lenses.push(
        new vscode.CodeLens(range, {
          title,
          command: "graphHarness.testSelectorAtCursor",
          arguments: [name],
        }),
      );
    }
    return lenses;
  }
}

// --- Diagnostics from validate-diff ----------------------------------------

const diagnostics = vscode.languages.createDiagnosticCollection("graph-harness");

function severityFromString(s: string): vscode.DiagnosticSeverity {
  switch (s) {
    case "critical":
    case "high":
      return vscode.DiagnosticSeverity.Error;
    case "medium":
      return vscode.DiagnosticSeverity.Warning;
    case "low":
      return vscode.DiagnosticSeverity.Information;
  }
  return vscode.DiagnosticSeverity.Hint;
}

async function runValidateDiffOnGitChanges(): Promise<void> {
  const folder = vscode.workspace.workspaceFolders?.[0];
  if (!folder) return;
  let diff = "";
  try {
    const out = await execAsync("git diff", { cwd: folder.uri.fsPath, maxBuffer: 16 * 1024 * 1024 });
    diff = out.stdout;
  } catch {
    return;
  }
  if (!diff.trim()) {
    diagnostics.clear();
    return;
  }
  let res: any;
  try {
    res = await validateDiff(diff, getOptions());
  } catch (e) {
    vscode.window.showErrorMessage(`graph-harness: validate-diff failed: ${e}`);
    return;
  }
  diagnostics.clear();
  const byUri = new Map<string, vscode.Diagnostic[]>();
  for (const f of res.findings || []) {
    // Until findings carry hunk → line mapping (P3+), pin to file
    // line 0 with the qualified name in the message.
    const fpath = (f.subject?.entity_id || "").toString();
    const uri = vscode.Uri.file(`${folder.uri.fsPath}/${fpath}`);
    const d = new vscode.Diagnostic(
      new vscode.Range(0, 0, 0, 1),
      `${f.kind}: ${f.subject?.qualified_name || "?"} (${f.subject?.flow || "?"})`,
      severityFromString(f.severity || "medium"),
    );
    d.source = "graph-harness";
    const arr = byUri.get(uri.toString()) || [];
    arr.push(d);
    byUri.set(uri.toString(), arr);
  }
  for (const [u, arr] of byUri) {
    diagnostics.set(vscode.Uri.parse(u), arr);
  }
}

// --- activation ------------------------------------------------------------

// Module-level handle so `deactivate` can shut down the JSON-RPC client.
let activeClient: JsonRpcClient | null = null;

export function activate(ctx: vscode.ExtensionContext) {
  const lensProvider = new GHCodeLensProvider();
  ctx.subscriptions.push(
    vscode.languages.registerCodeLensProvider({ language: "graph-harness" }, lensProvider),
    diagnostics,
  );

  // --- Framework code lens + push subscription (P2.T40 / P2.T40a) ----------
  //
  // Spin up a JSON-RPC client per workspace folder, identify with a stable
  // subscriber id (so cursors survive reconnect), and subscribe to the
  // "code.framework" filter. The DaemonBridge wires push events into a
  // FrameworkStore; the FrameworkLensProvider re-renders the affected
  // documents via its onDidChangeCodeLenses emitter — no polling.
  const folder = vscode.workspace.workspaceFolders?.[0];
  if (folder) {
    const root = discoverWorkspaceRoot(folder.uri.fsPath) ?? folder.uri.fsPath;
    const sockPath = socketPath(root);
    const subscriberId = "vscode:" + extensionInstanceId(ctx);
    const bridge = new DaemonBridge({ subscriberId });
    const store = new FrameworkStore({ query: bridge.buildQuery() });
    bridge.attachStore(store);
    const fwClient = new JsonRpcClient(
      unixSocketConnector(sockPath),
      bridge.clientOptions(),
    );
    bridge.setClient(fwClient);
    activeClient = fwClient;
    fwClient.start();

    const fwLens = new FrameworkLensProvider(store);
    ctx.subscriptions.push(
      vscode.languages.registerCodeLensProvider(
        [
          { language: "go", scheme: "file" },
          { language: "typescript", scheme: "file" },
          { language: "typescriptreact", scheme: "file" },
          { language: "javascript", scheme: "file" },
          { language: "javascriptreact", scheme: "file" },
          { language: "python", scheme: "file" },
          { language: "prisma", scheme: "file" },
        ],
        fwLens,
      ),
      fwLens,
      vscode.commands.registerCommand("graphHarness.framework.showDetails", (state, info) => {
        const lines: string[] = [];
        lines.push(`Entity: ${state?.key ?? "(unknown)"}`);
        if (info) {
          if (info.flow_names?.length) {
            lines.push("Flows:");
            for (const n of info.flow_names) lines.push(`  - ${n}`);
          }
          if (typeof info.bound_flows === "number") lines.push(`Bound flows: ${info.bound_flows}`);
          if (typeof info.active_findings === "number") lines.push(`Active findings: ${info.active_findings}`);
          if (typeof info.subscribers === "number") lines.push(`Subscribers: ${info.subscribers}`);
        }
        vscode.window.showInformationMessage(lines.join("\n"));
      }),
      { dispose: () => fwClient.stop() },
    );
  }

  ctx.subscriptions.push(
    vscode.commands.registerCommand("graphHarness.validateDiff", () => runValidateDiffOnGitChanges()),
    vscode.commands.registerCommand("graphHarness.testSelectorAtCursor", async (name?: string) => {
      const ed = vscode.window.activeTextEditor;
      let target = name;
      if (!target && ed) {
        const wordRange = ed.document.getWordRangeAtPosition(ed.selection.active);
        if (wordRange) target = ed.document.getText(wordRange);
      }
      if (!target) {
        vscode.window.showWarningMessage("graph-harness: no selector under cursor");
        return;
      }
      const res = await selectorTest(target, getOptions(ed?.document));
      if (res === null) {
        vscode.window.showErrorMessage("graph-harness: daemon unavailable");
      } else {
        vscode.window.showInformationMessage(
          `${target}: ${res.matches} match${res.matches === 1 ? "" : "es"}`,
        );
      }
    }),
    vscode.commands.registerCommand("graphHarness.openStudio", async () => {
      const opts = getOptions();
      try {
        const out = await runCLI(["studio", "--oneshot", "--port", "0"], opts);
        const m = out.match(/http:\/\/[^\s]+/);
        if (m) {
          await vscode.env.openExternal(vscode.Uri.parse(m[0]));
        }
      } catch (e) {
        vscode.window.showErrorMessage(`graph-harness: studio failed: ${e}`);
      }
    }),
  );

  // Re-run validate-diff on save for any tracked language. Cheap because
  // the daemon caches the code.core index.
  ctx.subscriptions.push(
    vscode.workspace.onDidSaveTextDocument((doc) => {
      const tracked = ["go", "typescript", "typescriptreact", "javascript", "javascriptreact", "python"];
      if (!tracked.includes(doc.languageId)) return;
      runValidateDiffOnGitChanges().catch(() => undefined);
    }),
  );

  // Refresh code lenses whenever a .gh file is saved.
  ctx.subscriptions.push(
    vscode.workspace.onDidSaveTextDocument((doc) => {
      if (doc.languageId === "graph-harness") lensProvider.refresh();
    }),
  );
}

export function deactivate() {
  diagnostics.dispose();
  if (activeClient) {
    activeClient.stop();
    activeClient = null;
  }
}

// Stable per-install id used as the subscriber_id suffix. Persisted in
// workspaceState so cursor lookups survive extension reloads (the daemon
// keys cross-restart cursors on subscriber_id + subscription_name).
function extensionInstanceId(ctx: vscode.ExtensionContext): string {
  const KEY = "graphHarness.instanceId";
  let id = ctx.workspaceState.get<string>(KEY);
  if (!id) {
    id = crypto.randomBytes(8).toString("hex");
    void ctx.workspaceState.update(KEY, id);
  }
  return id;
}
