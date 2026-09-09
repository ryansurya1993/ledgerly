package db

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Connect opens the connection pool the service uses for all runtime
// queries, authenticated as the restricted ledger_app role (Config.AppDSN).
// It pings once up front so a misconfigured or unreachable database fails
// service startup immediately, instead of surfacing as a mysterious error
// on the first real request.
//
// DefaultQueryExecMode is forced to QueryExecModeExec, overriding pgx's
// own default (QueryExecModeCacheStatement), because AppDSN may point at
// PgBouncer in transaction-pooling mode (see MigrateURL's doc comment
// for why that mode matters, and docker-compose.yml for where this
// project actually puts PgBouncer in front of AppDSN). The default mode
// caches server-side prepared statements on the assumption that the
// same backend connection will still have them the next time they're
// needed; under transaction pooling, PgBouncer can hand this pool's
// next transaction to a *different* backend that never saw that
// PREPARE, which surfaces as "prepared statement does not exist"
// errors under load. QueryExecModeExec uses the extended protocol
// without naming or reusing statements across round trips, so it works
// the same way regardless of whether AppDSN is PgBouncer or Postgres
// itself -- unlike QueryExecModeSimpleProtocol (pgx's other commonly
// suggested option for connection poolers), it keeps native
// binary-format results and Go-type-based parameter encoding, so this
// is a pure compatibility fix with no other behavior change here.
func Connect(ctx context.Context, cfg Config) (*pgxpool.Pool, error) {
	poolConfig, err := pgxpool.ParseConfig(cfg.AppDSN())
	if err != nil {
		return nil, err
	}
	poolConfig.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, err
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}

	return pool, nil
}
