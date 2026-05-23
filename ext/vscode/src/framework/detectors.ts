// Source-side detectors: scan a TextDocument and return the line ranges
// where a framework entity (handler / publisher / schema field) is
// declared, plus a heuristic "qualified name" key the daemon can resolve
// to a code.framework SelectorRef.
//
// These regex-level matches are intentionally lossy — they exist to know
// *where to put a code lens*. The authoritative entity resolution happens
// daemon-side via `framework.routes` / `framework.event_publishers` /
// `framework.schema_fields`; the lens just attaches the daemon's answer to
// the right source range.
//
// We keep the patterns conservative: false positives waste a lens query;
// false negatives leave a line bare. The daemon answers an unknown anchor
// with an empty result so the worst case is a missing lens, not a wrong one.

export type DetectedKind = "handler" | "event_publisher" | "schema_field";

export interface DetectedSite {
  kind: DetectedKind;
  /** Zero-based line number of the detection. */
  line: number;
  /** A heuristic key the daemon can lookup; usually the qualified name. */
  key: string;
}

export interface LineDocument {
  readonly languageId: string;
  readonly lineCount: number;
  lineAt(i: number): { text: string };
}

// --- Handlers --------------------------------------------------------------
//
// We can't reliably classify "this function is bound to a Route" from
// source alone — that's the daemon's job. What we *can* do is highlight
// functions exported from files under HTTP-handler directory conventions
// (handlers/, routes/, controllers/, api/) and let the daemon decide if
// they're bound. A function with no Route binding gets "0 bound flows".

const GO_FUNC_RE = /^\s*func(?:\s+\([^)]*\))?\s+([A-Z][A-Za-z0-9_]*)\s*\(/;
const TS_FUNC_RE = /^\s*(?:export\s+)?(?:async\s+)?function\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(/;
const TS_ARROW_RE = /^\s*(?:export\s+)?const\s+([A-Za-z_][A-Za-z0-9_]*)\s*[:=]\s*(?:async\s*)?\(/;
const PY_DEF_RE = /^\s*(?:async\s+)?def\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(/;

const HANDLER_DIRS = ["handlers", "routes", "controllers", "api", "endpoints"];

function isLikelyHandlerFile(filepath: string): boolean {
  const lower = filepath.toLowerCase().replace(/\\/g, "/");
  return HANDLER_DIRS.some((d) => lower.includes("/" + d + "/") || lower.includes("/" + d + "."));
}

function detectHandlerLines(doc: LineDocument, filepath: string): DetectedSite[] {
  if (!isLikelyHandlerFile(filepath)) return [];
  const out: DetectedSite[] = [];
  const isGo = doc.languageId === "go";
  const isTS =
    doc.languageId === "typescript" ||
    doc.languageId === "typescriptreact" ||
    doc.languageId === "javascript" ||
    doc.languageId === "javascriptreact";
  const isPy = doc.languageId === "python";
  for (let i = 0; i < doc.lineCount; i++) {
    const text = doc.lineAt(i).text;
    let name: string | null = null;
    if (isGo) {
      const m = GO_FUNC_RE.exec(text);
      if (m) name = m[1];
    } else if (isTS) {
      const m = TS_FUNC_RE.exec(text) || TS_ARROW_RE.exec(text);
      if (m) name = m[1];
    } else if (isPy) {
      const m = PY_DEF_RE.exec(text);
      if (m) name = m[1];
    }
    if (name) {
      out.push({ kind: "handler", line: i, key: name });
    }
  }
  return out;
}

// --- Event publishers ------------------------------------------------------
//
// Heuristic patterns for the dominant publisher constructor / send call
// sites across Go (segmentio/kafka-go, IBM/sarama, Shopify/sarama,
// nats.io/nats.go, rabbitmq/amqp091-go), TS (kafkajs, amqplib,
// @nats-io/nats), and Python (confluent-kafka, aio-pika, nats-py).
//
// One match per line; we use the matched call expression as the key so
// the daemon can join against its EventPublisher.AnchoredTo entries.

const PUBLISHER_PATTERNS: RegExp[] = [
  // Go — kafka-go / sarama / nats.go / amqp091
  /\bkafka\.NewWriter\b/,
  /\bsarama\.NewSyncProducer\b/,
  /\bsarama\.NewAsyncProducer\b/,
  /\bnats\.Connect\b/,
  /\bamqp\.Dial\b/,
  // TS — kafkajs / amqplib / nats
  /\.producer\s*\(\s*\)/,
  /\b(?:producer|kafka)\.send\s*\(/,
  /\bchannel\.publish\s*\(/,
  /\bnc\.publish\s*\(/,
  // Python — confluent-kafka / aio-pika / nats-py
  /\bProducer\s*\(/,
  /\bproducer\.produce\s*\(/,
  /\b(?:exchange|channel)\.publish\s*\(/,
  /\bnc\.publish\s*\(/,
];

function detectPublisherLines(doc: LineDocument): DetectedSite[] {
  const out: DetectedSite[] = [];
  for (let i = 0; i < doc.lineCount; i++) {
    const text = doc.lineAt(i).text;
    if (text.trim().startsWith("//") || text.trim().startsWith("#")) continue;
    for (const re of PUBLISHER_PATTERNS) {
      const m = re.exec(text);
      if (m) {
        out.push({ kind: "event_publisher", line: i, key: m[0] });
        break;
      }
    }
  }
  return out;
}

// --- Schema fields ---------------------------------------------------------
//
// Per-language schema-DSL line shapes:
//   - Prisma: `  email String @unique` inside a `model X { ... }` block
//   - Drizzle: `email: varchar('email'…)`/`text('email')`
//   - SQLAlchemy: `email = Column(String, …)` / `email: Mapped[str]`
//   - GORM: `Email string \`gorm:"…"\`` (we lean on the gorm tag)

const PRISMA_FIELD_RE = /^\s*([a-z_][A-Za-z0-9_]*)\s+[A-Z][A-Za-z0-9]*(?:\?|\[\])?\s*(?:@.*)?$/;
const DRIZZLE_FIELD_RE = /^\s*([a-z_][A-Za-z0-9_]*)\s*:\s*(?:varchar|text|integer|int|serial|boolean|timestamp|json|jsonb|uuid|numeric|real|date|time|bigint)\s*\(/i;
const SQLA_COLUMN_RE = /^\s*([a-z_][A-Za-z0-9_]*)\s*(?::\s*Mapped\[[^\]]+\]\s*)?=\s*(?:mapped_column|Column)\s*\(/;
const GORM_TAG_RE = /`gorm:"[^"]*"`/;
const GORM_FIELD_RE = /^\s*([A-Z][A-Za-z0-9_]*)\s+[A-Za-z*\.\[\]_]+\s+`[^`]*gorm:"[^"]*"[^`]*`/;

function isPrismaDoc(filepath: string, languageId: string): boolean {
  return languageId === "prisma" || filepath.toLowerCase().endsWith(".prisma");
}

function isDrizzleDoc(doc: LineDocument, filepath: string): boolean {
  const lower = filepath.toLowerCase();
  if (!lower.endsWith(".ts") && !lower.endsWith(".js")) return false;
  // Look for the canonical drizzle-orm import as a guard.
  for (let i = 0; i < Math.min(doc.lineCount, 40); i++) {
    if (/from\s+["']drizzle-orm/.test(doc.lineAt(i).text)) return true;
  }
  return false;
}

function isSqlAlchemyDoc(doc: LineDocument, filepath: string): boolean {
  if (!filepath.toLowerCase().endsWith(".py")) return false;
  for (let i = 0; i < Math.min(doc.lineCount, 60); i++) {
    if (/from\s+sqlalchemy/.test(doc.lineAt(i).text)) return true;
  }
  return false;
}

function isGormDoc(doc: LineDocument, filepath: string): boolean {
  if (!filepath.toLowerCase().endsWith(".go")) return false;
  // Cheap signal: a `gorm:"..."` tag anywhere in the first 200 lines.
  for (let i = 0; i < Math.min(doc.lineCount, 200); i++) {
    if (GORM_TAG_RE.test(doc.lineAt(i).text)) return true;
  }
  return false;
}

function detectSchemaFieldLines(doc: LineDocument, filepath: string): DetectedSite[] {
  const out: DetectedSite[] = [];
  if (isPrismaDoc(filepath, doc.languageId)) {
    let inModel = false;
    for (let i = 0; i < doc.lineCount; i++) {
      const t = doc.lineAt(i).text;
      if (/^\s*model\s+\w+\s*\{/.test(t)) { inModel = true; continue; }
      if (inModel && /^\s*\}/.test(t)) { inModel = false; continue; }
      if (!inModel) continue;
      const m = PRISMA_FIELD_RE.exec(t);
      if (m) out.push({ kind: "schema_field", line: i, key: m[1] });
    }
    return out;
  }
  if (isDrizzleDoc(doc, filepath)) {
    for (let i = 0; i < doc.lineCount; i++) {
      const m = DRIZZLE_FIELD_RE.exec(doc.lineAt(i).text);
      if (m) out.push({ kind: "schema_field", line: i, key: m[1] });
    }
    return out;
  }
  if (isSqlAlchemyDoc(doc, filepath)) {
    for (let i = 0; i < doc.lineCount; i++) {
      const m = SQLA_COLUMN_RE.exec(doc.lineAt(i).text);
      if (m) out.push({ kind: "schema_field", line: i, key: m[1] });
    }
    return out;
  }
  if (isGormDoc(doc, filepath)) {
    for (let i = 0; i < doc.lineCount; i++) {
      const m = GORM_FIELD_RE.exec(doc.lineAt(i).text);
      if (m) out.push({ kind: "schema_field", line: i, key: m[1] });
    }
    return out;
  }
  return out;
}

// --- Public entry ----------------------------------------------------------

export function detectFrameworkSites(doc: LineDocument, filepath: string): DetectedSite[] {
  return [
    ...detectHandlerLines(doc, filepath),
    ...detectPublisherLines(doc),
    ...detectSchemaFieldLines(doc, filepath),
  ];
}
