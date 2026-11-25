package gonestly_test

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/yaboylino/gonestly"
)

type DB struct{ DSN string }

func NewDB() *DB { return &DB{DSN: "postgres://localhost/shop"} }

type UserService struct{ db *DB }

func NewUserService(db *DB) *UserService { return &UserService{db: db} }

var (
	DatabaseModule = &gonestly.Module{
		Name:      "database",
		Providers: gonestly.Providers(NewDB),
		Exports:   gonestly.Exports(NewDB),
	}
	UsersModule = &gonestly.Module{
		Name:      "users",
		Imports:   gonestly.Imports(DatabaseModule),
		Providers: gonestly.Providers(NewUserService),
		Exports:   gonestly.Exports(NewUserService),
	}
	AppModule = &gonestly.Module{
		Name:    "app",
		Imports: gonestly.Imports(UsersModule),
	}
)

func Example() {
	app, err := gonestly.New(AppModule, gonestly.WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		fmt.Println(err)
		return
	}
	if err := app.Start(context.Background()); err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = app.Stop(context.Background()) }()

	users, _ := gonestly.Resolve[*UserService](app)
	fmt.Println(users.db.DSN)
	// Output: postgres://localhost/shop
}

func ExampleNew_missingDependency() {
	users := &gonestly.Module{
		Name:      "users",
		Providers: gonestly.Providers(NewUserService),
	}
	app := &gonestly.Module{
		Name:    "app",
		Imports: gonestly.Imports(DatabaseModule, users),
	}

	_, err := gonestly.New(app)
	fmt.Println(err)
	// Output:
	// gonestly: the application cannot be built:
	//   - missing dependency: gonestly_test.NewUserService in module "users" needs *gonestly_test.DB, which is not available there; module "database" exports it: add that module to the Imports of "users"
}
