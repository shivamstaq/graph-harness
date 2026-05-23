// Minimal mock of the `vscode` runtime module surface used by the
// extension. node:test runs tests in plain Node, where `import "vscode"`
// would otherwise resolve to nothing. Tests that touch the vscode API
// install this mock via Node's --import flag (see package.json scripts).
//
// Only the shapes used by FrameworkLensProvider's pure helpers are
// implemented. We deliberately keep this small: anything richer should
// move into a VS Code-extension-host integration test (not a unit test).

import { Module } from "module";

// `vscode` is resolved relative to the test file, so we hijack
// Module._load to intercept it.
const RUNTIME = {
  Range: class {
    constructor(public startLine: number, public startChar: number, public endLine: number, public endChar: number) {}
  },
  CodeLens: class {
    public command: { title: string; command: string; arguments?: unknown[] } | undefined;
    constructor(public range: unknown, command?: { title: string; command: string; arguments?: unknown[] }) {
      this.command = command;
    }
  },
  EventEmitter: class<T> {
    private listeners: Array<(arg: T) => void> = [];
    event = (fn: (arg: T) => void) => {
      this.listeners.push(fn);
      return { dispose: () => { this.listeners = this.listeners.filter((l) => l !== fn); } };
    };
    fire(arg?: T) {
      for (const l of [...this.listeners]) l(arg as T);
    }
    dispose() { this.listeners = []; }
  },
  Uri: { file: (p: string) => ({ fsPath: p, toString: () => `file://${p}` }) },
  workspace: {},
  languages: {},
  window: {},
  commands: {},
  CancellationTokenSource: class {
    token = { isCancellationRequested: false, onCancellationRequested: () => ({ dispose: () => undefined }) };
    cancel() { this.token.isCancellationRequested = true; }
    dispose() {}
  },
};

type LoadFn = (...args: unknown[]) => unknown;
const origLoad = (Module as unknown as { _load: LoadFn })._load;
(Module as unknown as { _load: LoadFn })._load = function (this: unknown, ...args: unknown[]) {
  if (args[0] === "vscode") return RUNTIME;
  return origLoad.apply(this, args);
};
