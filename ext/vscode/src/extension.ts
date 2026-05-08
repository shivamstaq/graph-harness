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

export function activate(ctx: vscode.ExtensionContext) {
  const lensProvider = new GHCodeLensProvider();
  ctx.subscriptions.push(
    vscode.languages.registerCodeLensProvider({ language: "graph-harness" }, lensProvider),
    diagnostics,
  );

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
}
