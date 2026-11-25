package sql_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/yaboylino/gonestly/sql"
)

type Product struct {
	ID    int64  `db:"id"`
	Name  string `db:"name"`
	Price int64  `db:"price"`
}

var (
	productName  = sql.Col[string]("name")
	productPrice = sql.Col[int64]("price")
)

func Example() {
	ctx := context.Background()
	dir, _ := os.MkdirTemp("", "gonestly-example")
	defer os.RemoveAll(dir)

	db, err := sql.Open(ctx, sql.Options{Driver: "sqlite", DSN: "file:" + filepath.Join(dir, "shop.db")})
	if err != nil {
		fmt.Println(err)
		return
	}
	defer db.Close()
	_, _ = db.Exec(ctx, "CREATE TABLE products (id INTEGER PRIMARY KEY, name TEXT NOT NULL, price INTEGER NOT NULL)")

	products, _ := sql.NewRepository[Product, int64](db)
	for _, p := range []Product{{Name: "Tea", Price: 350}, {Name: "Coffee", Price: 450}, {Name: "Cake", Price: 600}} {
		_ = products.Create(ctx, &p)
	}

	cheap, _ := sql.From[Product](db).Where(productPrice.Lt(500)).OrderBy(productName.Asc()).All(ctx)
	for _, p := range cheap {
		fmt.Println(p.ID, p.Name, p.Price)
	}

	total, _ := sql.QueryOne[int64](ctx, db, "SELECT SUM(price) FROM products WHERE name IN (?)", []string{"Tea", "Cake"})
	fmt.Println("total:", total)
	// Output:
	// 2 Coffee 450
	// 1 Tea 350
	// total: 950
}
