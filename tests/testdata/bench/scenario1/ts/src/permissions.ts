// Permission middleware that gates the checkout route. The CheckoutValidator
// is invoked from inside this middleware so the call graph contains an
// auth-context → flow-scoped-function edge — the structure scenario 1
// scores against.

import type { Request, Response, NextFunction } from "express";
import { CheckoutValidator, type Cart } from "./checkout/validator";

const validator = new CheckoutValidator();

export function requireValidCart(req: Request, _res: Response, next: NextFunction) {
  const cart: Cart = req.body?.cart;
  validator.validate(cart);
  next();
}
