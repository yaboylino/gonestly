// Package jwt authenticates requests with JSON Web Tokens.
//
// Import Module in the root module, and inject *Tokens where tokens are signed or routes are
// protected:
//
//	func (c *Controller) Routes(r *gonestly.Router) {
//		r.Prefix("/orders").Guard(c.tokens.Guard())
//		gonestly.Get(r, "/", c.List)
//		gonestly.Delete(r, "/{id}", c.Delete, gonestly.WithGuards(c.tokens.Guard("admin")))
//	}
//
// A handler reads who is calling with ClaimsFrom.
package jwt
