package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/config"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/database"
)

type migrationOperation func(context.Context, config.Config) error

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(realMain(ctx, os.Args[1:], os.Stdout, os.Stderr, os.LookupEnv, migrateUp))
}

func realMain(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	lookup config.LookupEnv,
	operation migrationOperation,
) int {
	if len(args) != 1 || args[0] != "up" {
		fmt.Fprintln(stderr, "usage: paper-hub-migrate up")
		return 2
	}
	cfg, err := config.LoadFrom(config.RoleMigrate, lookup)
	if err != nil {
		fmt.Fprintf(stderr, "load migration configuration: %v\n", err)
		return 1
	}
	if err := operation(ctx, cfg); err != nil {
		fmt.Fprintf(stderr, "apply migrations: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "migrations applied")
	return 0
}

func migrateUp(ctx context.Context, cfg config.Config) error {
	pool, err := database.Open(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer pool.Close()
	return database.Up(ctx, pool)
}
