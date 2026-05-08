// Express app entry point. Wires the requireValidCart middleware in front
// of the /checkout route so the flow-scoped validator sits on the
// pre-payment path.

import express from "express";
import { requireValidCart } from "./permissions";

export function buildApp(): express.Express {
  const app = express();
  app.use(express.json());
  app.post("/checkout", requireValidCart, (_req, res) => {
    res.json({ status: "ok" });
  });
  return app;
}

if (require.main === module) {
  buildApp().listen(3000);
}
