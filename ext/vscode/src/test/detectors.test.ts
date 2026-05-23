// Unit tests for the source-side framework-site detectors. Each test
// builds a minimal in-memory LineDocument and asserts the detector picks
// out the expected line + key.

import { test } from "node:test";
import * as assert from "node:assert/strict";
import { detectFrameworkSites, type LineDocument } from "../framework/detectors";

function doc(languageId: string, source: string): LineDocument {
  const lines = source.split("\n");
  return {
    languageId,
    lineCount: lines.length,
    lineAt: (i) => ({ text: lines[i] ?? "" }),
  };
}

test("Go handler in handlers/ dir is detected", () => {
  const src = `package handlers

func ListUsers(w http.ResponseWriter, r *http.Request) {}
func privateHelper() {}
`;
  const sites = detectFrameworkSites(doc("go", src), "/repo/internal/handlers/users.go");
  // Only the exported function ListUsers should match.
  assert.deepEqual(sites.map((s) => [s.kind, s.key]), [["handler", "ListUsers"]]);
});

test("Go file outside a handler dir yields no handler matches", () => {
  const src = `package util
func ListUsers() {}
`;
  const sites = detectFrameworkSites(doc("go", src), "/repo/internal/util/helpers.go");
  assert.equal(sites.filter((s) => s.kind === "handler").length, 0);
});

test("TS arrow function in routes/ is detected", () => {
  const src = `import { Router } from 'express';
export const listUsers = async (req, res) => res.json([]);
`;
  const sites = detectFrameworkSites(doc("typescript", src), "/repo/src/routes/users.ts");
  assert.ok(sites.some((s) => s.kind === "handler" && s.key === "listUsers"));
});

test("Python async def in api/ is detected", () => {
  const src = `from fastapi import APIRouter
router = APIRouter()
async def list_users():
    return []
`;
  const sites = detectFrameworkSites(doc("python", src), "/repo/app/api/users.py");
  assert.ok(sites.some((s) => s.kind === "handler" && s.key === "list_users"));
});

test("kafka-go publisher call site is detected", () => {
  const src = `package events
import "github.com/segmentio/kafka-go"
func New() {
    w := kafka.NewWriter(kafka.WriterConfig{})
    _ = w
}
`;
  const sites = detectFrameworkSites(doc("go", src), "/repo/internal/events/writer.go");
  assert.ok(sites.some((s) => s.kind === "event_publisher" && s.key === "kafka.NewWriter"));
});

test("kafkajs producer.send call site is detected", () => {
  const src = `import { Kafka } from 'kafkajs';
const producer = kafka.producer();
await producer.send({ topic: 't' });
`;
  const sites = detectFrameworkSites(doc("typescript", src), "/repo/src/events/publish.ts");
  assert.ok(sites.some((s) => s.kind === "event_publisher" && /producer\.send/.test(s.key)));
});

test("Prisma schema fields are detected inside a model block", () => {
  const src = `model User {
  id    Int    @id @default(autoincrement())
  email String @unique
  name  String?
}
`;
  const sites = detectFrameworkSites(doc("prisma", src), "/repo/prisma/schema.prisma");
  const keys = sites.filter((s) => s.kind === "schema_field").map((s) => s.key).sort();
  assert.deepEqual(keys, ["email", "id", "name"]);
});

test("Drizzle schema fields are detected when drizzle-orm is imported", () => {
  const src = `import { pgTable, varchar, integer } from 'drizzle-orm/pg-core';
export const users = pgTable('users', {
  id: integer('id').primaryKey(),
  email: varchar('email', { length: 255 }).notNull(),
});
`;
  const sites = detectFrameworkSites(doc("typescript", src), "/repo/src/db/schema.ts");
  const keys = sites.filter((s) => s.kind === "schema_field").map((s) => s.key).sort();
  assert.deepEqual(keys, ["email", "id"]);
});

test("GORM tagged fields are detected", () => {
  const src = `package model
type User struct {
    ID    uint   \`gorm:"primaryKey"\`
    Email string \`gorm:"uniqueIndex"\`
}
`;
  const sites = detectFrameworkSites(doc("go", src), "/repo/internal/model/user.go");
  const keys = sites.filter((s) => s.kind === "schema_field").map((s) => s.key).sort();
  assert.deepEqual(keys, ["Email", "ID"]);
});

test("SQLAlchemy Column declarations are detected", () => {
  const src = `from sqlalchemy import Column, Integer, String
class User(Base):
    __tablename__ = 'users'
    id = Column(Integer, primary_key=True)
    email = Column(String, unique=True)
`;
  const sites = detectFrameworkSites(doc("python", src), "/repo/app/db/models.py");
  const keys = sites.filter((s) => s.kind === "schema_field").map((s) => s.key).sort();
  assert.deepEqual(keys, ["email", "id"]);
});
