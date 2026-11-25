package sql

import "context"

// Query runs a query and scans every row into a T. A struct T is filled by column name, using
// the db tags of its fields; any other T, such as int64 or string, is read from a single column.
//
//	users, err := sql.Query[User](ctx, db, "SELECT * FROM users WHERE team_id = ?", teamID)
func Query[T any](ctx context.Context, db *DB, query string, args ...any) ([]T, error) {
	rows, err := db.query(ctx, query, args)
	if err != nil {
		return nil, err
	}
	return scanRows[T](db, rows)
}

// QueryOne runs a query that returns exactly one row. It returns ErrNotFound for no rows and
// ErrTooManyRows for more than one.
func QueryOne[T any](ctx context.Context, db *DB, query string, args ...any) (T, error) {
	var zero T
	rows, err := Query[T](ctx, db, query, args...)
	switch {
	case err != nil:
		return zero, err
	case len(rows) == 0:
		return zero, ErrNotFound
	case len(rows) > 1:
		return zero, ErrTooManyRows
	default:
		return rows[0], nil
	}
}

// GroupBy groups loaded rows by a key, to attach related rows without one query per parent:
//
//	posts, err := sql.From[Post](db).Where(postUserID.In(userIDs...)).All(ctx)
//	postsByUser := sql.GroupBy(posts, func(p Post) int64 { return p.UserID })
func GroupBy[T any, K comparable](rows []T, key func(T) K) map[K][]T {
	groups := make(map[K][]T)
	for _, row := range rows {
		k := key(row)
		groups[k] = append(groups[k], row)
	}
	return groups
}
