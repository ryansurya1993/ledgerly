package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Connect opens the connection pool the service uses for all runtime
// queries, authenticated as the restricted ledger_app role (Config.AppDSN).
// It pings once up front so a misconfigured or unreachable database fails
// service startup immediately, instead of surfacing as a mysterious error
// on the first real request.
func Connect(ctx context.Context, cfg Config) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, cfg.AppDSN())
	if err != nil {
		return nil, err
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}

	return pool, nil
}
