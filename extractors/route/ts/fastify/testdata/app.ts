import Fastify from 'fastify';

const fastify = Fastify({ logger: true });

// Per-method shortcut with inline arrow handler.
fastify.get('/health', async (_req, _reply) => {
  return { status: 'ok' };
});

// Identifier-referenced handler.
async function listOrders(_req: any, _reply: any) {
  return [];
}
fastify.get('/orders', listOrders);

// Per-method shortcut where the second arg is an options bag
// carrying `handler`. Fastify treats both shapes as equivalent.
fastify.post('/orders', {
  schema: { body: { type: 'object' } },
  handler: listOrders,
});

// `.route({...})` form with a single method string.
fastify.route({
  method: 'PUT',
  url: '/orders/:id',
  handler: listOrders,
});

// `.route({...})` form with a method ARRAY — emits one Route per
// method, sharing the url + handler.
fastify.route({
  method: ['GET', 'HEAD'],
  url: '/orders/:id/snapshot',
  handler: listOrders,
});

// Plugin registration: routes inside the plugin are extracted from
// the plugin's own file when it parses. The register call itself
// emits no Route entity (see matchRegisterCall in extractor.go).
fastify.register(async function adminPlugin(instance) {
  instance.get('/health', async () => 'ok');
}, { prefix: '/admin' });

export { fastify };
