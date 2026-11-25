// Package sql is the SQL layer of Gonestly, built on database/sql.
//
// It works at three levels, from the most convenient to full control:
//
//   - Repository gives every table the usual create, read, update and delete operations.
//   - From builds typed queries from columns: From[User](db).Where(email.Eq(x)).All(ctx).
//   - Query and QueryOne run your own SQL and scan the rows straight into structs.
//
// Every query uses ? as its placeholder, whatever the database; the dialect rewrites it.
// Transactions travel through the context, so repositories and queries called with that
// context take part in them automatically.
package sql
