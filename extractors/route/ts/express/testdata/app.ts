import express from 'express';

const app = express();
const router = express.Router();

// Inline arrow handler — high confidence, identifier-anchored
// fallback unavailable (the handler is anonymous so the Route
// anchors via path_glob on the source file only).
app.get('/health', (_req, res) => {
  res.status(200).send('ok');
});

// Identifier-referenced handler — anchors via qualified_name.
function listUsers(req: any, res: any) {
  res.json([]);
}
app.get('/users', listUsers);

// POST with middleware + handler — handler is the LAST positional arg.
const requireAuth = (req: any, res: any, next: any) => next();
app.post('/users', requireAuth, function createUser(req, res) {
  res.status(201).json(req.body);
});

// PUT / PATCH / DELETE cover the remaining canonical verbs.
app.put('/users/:id', listUsers);
app.patch('/users/:id', listUsers);
app.delete('/users/:id', listUsers);

// Template literal path with no interpolation → still high confidence.
const apiBase = `/v2/widgets`;
app.get(`/v2/widgets`, listUsers);

// Sub-router mount: router declares its own routes, mounted via use().
router.get('/profile', listUsers);

// Member-expression handler — qualified_name anchors to the dotted form.
const controllers = { admin: { metrics: listUsers } };
app.get('/admin/metrics', controllers.admin.metrics);

app.use('/api', router);

export { app, apiBase };
