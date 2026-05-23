// Package all blank-imports every code.framework extractor so their
// init() Register calls activate when the daemon binary loads.
//
// The daemon (cmd/graph-harness) imports this package once; downstream
// callers of code_framework.Registered() / code_framework.Descriptors()
// then see the full v1 extractor catalog.
//
// To disable a family at build time, remove its blank import below and
// rebuild the binary. Runtime enable/disable lives in
// .graph-harness/extractors.toml — see `graph-harness extractors
// {enable,disable}`.
//
// Per Phase 2 implementation strategy: this file is owned by the
// orchestrator. Pass-1 extractor agents own only their family subtree;
// they self-register via init() and the blank import here is what
// brings them into the binary.
package all

import (
	// Routes — Go
	_ "github.com/shivamstaq/graph-harness/extractors/route/go/chi"
	_ "github.com/shivamstaq/graph-harness/extractors/route/go/gin"
	_ "github.com/shivamstaq/graph-harness/extractors/route/go/gorillamux"
	_ "github.com/shivamstaq/graph-harness/extractors/route/go/nethttp"

	// Routes — TypeScript
	_ "github.com/shivamstaq/graph-harness/extractors/route/ts/express"
	_ "github.com/shivamstaq/graph-harness/extractors/route/ts/fastify"

	// Routes — Python
	_ "github.com/shivamstaq/graph-harness/extractors/route/py/django"
	_ "github.com/shivamstaq/graph-harness/extractors/route/py/fastapi"
	_ "github.com/shivamstaq/graph-harness/extractors/route/py/flask"

	// GraphQL — TypeScript
	_ "github.com/shivamstaq/graph-harness/extractors/graphql/ts/apollo"
	_ "github.com/shivamstaq/graph-harness/extractors/graphql/ts/graphqljs"
	_ "github.com/shivamstaq/graph-harness/extractors/graphql/ts/typegraphql"

	// GraphQL — Python
	_ "github.com/shivamstaq/graph-harness/extractors/graphql/py/ariadne"
	_ "github.com/shivamstaq/graph-harness/extractors/graphql/py/graphene"
	_ "github.com/shivamstaq/graph-harness/extractors/graphql/py/strawberry"

	// GraphQL — Go
	_ "github.com/shivamstaq/graph-harness/extractors/graphql/go/gqlgen"

	// Events — Kafka
	_ "github.com/shivamstaq/graph-harness/extractors/events/kafka/go"
	_ "github.com/shivamstaq/graph-harness/extractors/events/kafka/py"
	_ "github.com/shivamstaq/graph-harness/extractors/events/kafka/ts"

	// Events — NATS
	_ "github.com/shivamstaq/graph-harness/extractors/events/nats/go"
	_ "github.com/shivamstaq/graph-harness/extractors/events/nats/py"
	_ "github.com/shivamstaq/graph-harness/extractors/events/nats/ts"

	// Events — AMQP / RabbitMQ
	_ "github.com/shivamstaq/graph-harness/extractors/events/amqp/go"
	_ "github.com/shivamstaq/graph-harness/extractors/events/amqp/py"
	_ "github.com/shivamstaq/graph-harness/extractors/events/amqp/ts"

	// Events — Redis pub/sub
	_ "github.com/shivamstaq/graph-harness/extractors/events/redispubsub/go"
	_ "github.com/shivamstaq/graph-harness/extractors/events/redispubsub/py"
	_ "github.com/shivamstaq/graph-harness/extractors/events/redispubsub/ts"

	// Events — AWS SNS + SQS
	_ "github.com/shivamstaq/graph-harness/extractors/events/snsqs/go"
	_ "github.com/shivamstaq/graph-harness/extractors/events/snsqs/py"
	_ "github.com/shivamstaq/graph-harness/extractors/events/snsqs/ts"

	// Schemas — DSL / ORM model definitions
	_ "github.com/shivamstaq/graph-harness/extractors/schema/drizzle"
	_ "github.com/shivamstaq/graph-harness/extractors/schema/gorm"
	_ "github.com/shivamstaq/graph-harness/extractors/schema/prisma"
	_ "github.com/shivamstaq/graph-harness/extractors/schema/rawsql"
	_ "github.com/shivamstaq/graph-harness/extractors/schema/sqlalchemy"

	// Schemas — Migrations
	_ "github.com/shivamstaq/graph-harness/extractors/schema/migrations/alembic"
	_ "github.com/shivamstaq/graph-harness/extractors/schema/migrations/drizzlekit"
	_ "github.com/shivamstaq/graph-harness/extractors/schema/migrations/goose"
	_ "github.com/shivamstaq/graph-harness/extractors/schema/migrations/prismamigrate"

	// Tests
	_ "github.com/shivamstaq/graph-harness/extractors/test/go/gotest"
	_ "github.com/shivamstaq/graph-harness/extractors/test/py/pytest"
	_ "github.com/shivamstaq/graph-harness/extractors/test/ts/jest"
	_ "github.com/shivamstaq/graph-harness/extractors/test/ts/vitest"

	// Generated artifacts
	_ "github.com/shivamstaq/graph-harness/extractors/generated/heuristics"
	_ "github.com/shivamstaq/graph-harness/extractors/generated/manifest"
)
