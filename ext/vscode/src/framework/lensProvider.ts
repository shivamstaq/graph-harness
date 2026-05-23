// FrameworkLensProvider attaches "N bound flows" / "N active findings"
// (and "N subscribers" for publishers) lenses above framework-entity
// declarations.
//
// The provider owns no polling; it reacts to:
//   - `onDidChangeCodeLenses` fired by FrameworkStore.onChanged when a
//     push event invalidates a uri's cached info, and
//   - VS Code's own document-edit triggers (the editor calls
//     provideCodeLenses again when the doc changes).
//
// All daemon I/O is async; provideCodeLenses returns immediately with a
// placeholder lens for every detected site, and uses resolveCodeLens to
// fetch the actual counts on demand (VS Code only resolves visible lenses,
// which keeps the per-render cost bounded).

import * as vscode from "vscode";
import { detectFrameworkSites, type DetectedKind } from "./detectors";
import type { FrameworkStore, EntityKey } from "./store";
import type { EntityLensInfo } from "./types";

interface LensState {
  uri: string;
  key: EntityKey;
  kind: DetectedKind;
}

const STATE = new WeakMap<vscode.CodeLens, LensState>();

export class FrameworkLensProvider implements vscode.CodeLensProvider {
  private readonly _onDidChangeCodeLenses = new vscode.EventEmitter<void>();
  readonly onDidChangeCodeLenses = this._onDidChangeCodeLenses.event;

  private readonly storeListener: { dispose(): void };

  constructor(private readonly store: FrameworkStore) {
    // Re-render whenever the store reports any URI's lenses went stale.
    this.storeListener = store.onChanged(() => this._onDidChangeCodeLenses.fire());
  }

  dispose(): void {
    this._onDidChangeCodeLenses.dispose();
    this.storeListener.dispose();
  }

  provideCodeLenses(
    doc: vscode.TextDocument,
    _token: vscode.CancellationToken,
  ): vscode.CodeLens[] {
    const cfg = vscode.workspace.getConfiguration("graphHarness");
    if (!cfg.get<boolean>("framework.codeLens.enabled", true)) {
      return [];
    }
    const sites = detectFrameworkSites(
      {
        languageId: doc.languageId,
        lineCount: doc.lineCount,
        lineAt: (i) => ({ text: doc.lineAt(i).text }),
      },
      doc.uri.fsPath,
    );
    const lenses: vscode.CodeLens[] = [];
    for (const site of sites) {
      const range = new vscode.Range(site.line, 0, site.line, 0);
      const k: EntityKey = `${site.kind}:${site.key}`;
      const lens = new vscode.CodeLens(range);
      STATE.set(lens, { uri: doc.uri.toString(), key: k, kind: site.kind });
      lenses.push(lens);
    }
    return lenses;
  }

  async resolveCodeLens(
    lens: vscode.CodeLens,
    _token: vscode.CancellationToken,
  ): Promise<vscode.CodeLens> {
    const state = STATE.get(lens);
    if (!state) {
      lens.command = { title: "", command: "" };
      return lens;
    }
    let info: EntityLensInfo | null;
    try {
      info = await this.store.lookup(state.uri, state.key);
    } catch (e) {
      lens.command = {
        title: `$(warning) graph-harness: ${(e as Error).message}`,
        command: "",
      };
      return lens;
    }
    lens.command = buildCommand(state, info);
    return lens;
  }
}

export function buildCommand(state: LensState, info: EntityLensInfo | null): vscode.Command {
  if (!info) {
    return {
      title: "$(circle-slash) no framework binding",
      command: "graphHarness.framework.showDetails",
      arguments: [state],
    };
  }
  const flows = info.bound_flows ?? 0;
  const findings = info.active_findings ?? 0;
  const parts: string[] = [];
  parts.push(`${flows} bound flow${flows === 1 ? "" : "s"}`);
  parts.push(`${findings} active finding${findings === 1 ? "" : "s"}`);
  if (state.kind === "event_publisher" && typeof info.subscribers === "number") {
    parts.push(`${info.subscribers} subscriber${info.subscribers === 1 ? "" : "s"}`);
  }
  return {
    title: parts.join("  ·  "),
    command: "graphHarness.framework.showDetails",
    arguments: [state, info],
  };
}
