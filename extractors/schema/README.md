# Schema-family extractors

This subtree owns the **DB schema** producers in code.framework:
`Schema`, `SchemaField`, `SchemaRead`, `SchemaWrite`, and `Migration`
(per `internal/code_framework/types.go`). Per the v1 contract one Go
package per (tool, language); each package's `init()` calls
`code_framework.Register`.

| Subdir                          | Registry name              | Inputs                  | Emits                                                  | Languages       |
| ------------------------------- | -------------------------- | ----------------------- | ------------------------------------------------------ | --------------- |
| `prisma/`                       | `schema.prisma`            | `code.core.FileChanged` | `Schema`, `SchemaField`                                | TS / Py / Go    |
| `drizzle/`                      | `schema.drizzle`           | `code.core.FileChanged` | `Schema`, `SchemaField`                                | TS              |
| `sqlalchemy/`                   | `schema.sqlalchemy`        | `code.core.FileChanged` | `Schema`, `SchemaField`                                | Python          |
| `gorm/`                         | `schema.gorm`              | `code.core.FileChanged` | `Schema`, `SchemaField`                                | Go              |
| `rawsql/`                       | `schema.rawsql`            | `code.core.FileChanged` | `Schema`, `SchemaField`, `SchemaRead`, `SchemaWrite`   | Go / TS / Py    |
| `migrations/goose/`             | `migrations.goose`         | `code.core.FileChanged` | `Migration`                                            | Go              |
| `migrations/alembic/`           | `migrations.alembic`       | `code.core.FileChanged` | `Migration`                                            | Python          |
| `migrations/prismamigrate/`     | `migrations.prismamigrate` | `code.core.FileChanged` | `Migration`                                            | TS / Py / Go    |
| `migrations/drizzlekit/`        | `migrations.drizzlekit`    | `code.core.FileChanged` | `Migration`                                            | TS              |

## Anchor strategy

- `Schema`       → `qualified_name = <table>` (and `language_id` +
  `path_glob` disambiguators on the language-specific extractors).
- `SchemaField`  → `qualified_name = <table>.<field>` — matches the
  Pass-0.5-A convention in `internal/semantic_overlay/anchors/schema.go`.
- `SchemaRead` / `SchemaWrite` → anchored on `path_glob` and reference
  the field via the `<table>.<field>` qualified-name encoded in
  `FieldID` (suppress-at-source folds these with the SchemaField
  emission once both are present).

## Raw SQL caveat (v1 limit)

The plan's `risks` row called for sqlglot hosted under wazero+WASM, with
a subprocess fallback. **We did not ship that for v1.** The `rawsql`
package uses pure-Go regex + a hand-rolled string-literal walker —
zero cgo, zero sqlite-vec. Trade-offs:

- Recognises `CREATE TABLE`, `INSERT INTO`, `UPDATE … SET …`, and
  `SELECT … FROM …`. Other DML / DDL forms (`ALTER TABLE`, `DELETE`,
  CTEs, window functions, subqueries) are ignored at the top level —
  they don't break parsing but don't produce extra reads/writes.
- Quoted column names (`"col"` / `` `col` ``) are honoured; schema
  qualifiers (`public.foo`) are stripped to the bare table name.
- Multi-statement strings are parsed left-to-right; statements run
  through each per-shape recogniser independently.
- Source-file string literals: single-quote, double-quote, backtick,
  and Python triple-quoted strings are scanned. Tagged template
  literals and PEP-3101 f-string interpolations are read as opaque
  text (we don't substitute placeholders before parsing).
- We do NOT track column-type widths (`VARCHAR(255)` is reported as
  `VARCHAR(255)` whole; consumers that care about precision should
  parse the `DataType` field themselves).

Projects that need fuller SQL precision can wait for the
sqlglot-under-wazero follow-on (post-P2.5 risk row) or override the
extractor for `.sql` files specifically.

## Per-tool limits

### Prisma

- Relation fields (`posts Post[]`) are skipped — they're the
  semantic-overlay relations layer's job, not code.framework.
- Composite block markers (`@@id`, `@@unique`, `@@map`) at the model
  level are skipped — they alter table identity but the table name
  itself remains the source of truth for the anchor.

### Drizzle

- Dynamic table names (`pgTable(TABLE_NAME, ...)` with a non-literal
  first argument) are skipped.
- Column types are detected by the helper function name (`integer`,
  `text`, `varchar`, ...); custom type helpers will surface under
  their JS identifier and may need a config override downstream.

### SQLAlchemy

- Base class detection is name-only (`Base`, `DeclarativeBase`,
  `db.Model`). Projects that rename `Base` need a per-project override
  (a Pass-2.5 follow-on adds this).
- Imperative table mappings (`Table("t", metadata, ...)`) are not
  parsed.

### GORM

- Table name detection: explicit `func (Foo) TableName() string`
  receivers win. Otherwise we use snake_case + naive pluralisation
  (`s` suffix, `y → ies`). Custom `NamingStrategy` overrides are not
  honoured.
- Embedded `gorm.Model` is expanded to its 4 fields (`ID`, `CreatedAt`,
  `UpdatedAt`, `DeletedAt`).
- Pointer-type fields are marked `Nullable: true`; non-pointer fields
  default to `Nullable: false` unless an explicit `gorm:"-"` (skip) or
  no `not null` tag is present.

### Migrations

- All four extractors are filename-driven: the Version is the leading
  digit-run (Goose / Drizzle Kit) or the alembic revision identifier.
- Goose SQL files MUST contain a `-- +goose Up` marker; Go files MUST
  contain a `goose.AddMigration` call. Without those, we don't claim
  the file.
- Prisma Migrate detection is layout-only (parent dir
  `<ts>_<name>/migration.sql`); the file's contents are never read.
- Alembic requires the file under a `versions/` segment plus either
  a `revision = "..."` literal or a `def upgrade(` body.

## Adding a new schema extractor

1. Create `extractors/schema/<tool>/<tool>.go` with a package matching
   the directory and an `init()` that calls
   `code_framework.Register("schema.<tool>", New, Descriptor{...})`.
2. Implement the `code_framework.Extractor` interface and use the
   helpers in `extractors/schema/common/` to shape Schema /
   SchemaField / SchemaRead / SchemaWrite / Migration events.
3. Ship one fixture under the package's `_test.go` exercising the
   detection contract end-to-end via `Parse` (the test entrypoint
   every extractor exposes).
4. Update this README's table + per-tool limits section.
