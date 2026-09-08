// Package ledgerclient is a thin HTTP client for ledger-service's API.
// wallet-service never touches Postgres directly -- per CLAUDE.md's
// architecture section, all ledger state lives behind ledger-service,
// and this package is the only thing in wallet-service that talks to
// it. Everything here mirrors ledger-service's actual JSON contract
// (see ledger-service/README.md's API section) rather than importing
// its Go types directly -- the two services are separate Go modules on
// purpose (independent dependency sets, independent deploys), so this
// package's types are wallet-service's own copy of that contract, kept
// in sync by hand.
package ledgerclient
