package ledger

import (
	"context"
	"log"
)

// EventPublisher is how PostTransaction announces a newly posted
// transaction to the outside world -- currently, notification-service's
// live activity feed. A nil EventPublisher is valid and means "don't
// publish"; most tests, and any deployment that doesn't need the
// notification pipeline, can leave it unset.
type EventPublisher interface {
	PublishTransactionPosted(ctx context.Context, txn Transaction, entries []Entry) error
}

// publishTransactionPosted notifies l.publisher (if any) that txn was
// just committed. Called from PostTransaction's success path only --
// never for a replayed call, and never before the attempt that
// produced txn has actually committed (see PostTransaction's call
// site) -- so by the time this runs, the write it describes is already
// durable in Postgres regardless of what happens here.
//
// # Why best-effort-after-commit, not a transactional outbox
//
// The alternative would be a transactional outbox: write the event to
// an outbox table in the *same* Postgres transaction as the ledger
// entries, then a separate relay process reads that table and
// publishes to RabbitMQ, retrying indefinitely until it succeeds. That
// buys at-least-once delivery even across a crash between commit and
// publish. It's the right call when a consumer's correctness depends
// on never missing an event -- it is not the right call here.
// notification-service drives a live activity feed: a nice-to-have
// view onto state Postgres already owns authoritatively (GetHistory,
// GetBalance, and the /integrity endpoints are always the real
// answer), not a system anything else depends on for correctness. A
// crash in the narrow window between this transaction's commit and
// this function's publish call would drop one feed update; it would
// never drop, duplicate, or corrupt a ledger entry. Paying for outbox
// machinery -- an extra table, a relay process, dedup on the consumer
// side -- to protect a view nobody relies on for correctness is
// exactly the kind of over-engineering CLAUDE.md's "smallest tool that
// fits current scope" guidance warns against. If notification-service
// ever grows a reason to need delivery guarantees (e.g. it starts
// driving something that must not silently miss events), that's the
// signal to revisit this and add an outbox -- see the root README's
// Scaling Roadmap.
//
// This call is synchronous (not fired into its own goroutine): it
// blocks PostTransaction's return by however long the publish attempt
// takes, bounded by EventPublisher's own dial/publish timeout. An
// async fire-and-forget goroutine would shave that latency off every
// request, at the cost of unbounded concurrent publish attempts under
// load and a request that can no longer be said to have "finished"
// deterministically -- given this project's traffic level, bounded
// synchronous latency is the simpler, safer tradeoff. Either way, the
// error is deliberately swallowed here, not returned: this function's
// contract is "try, log, move on," so PostTransaction's caller never
// sees a request fail because a notification didn't go out.
func (l *Ledger) publishTransactionPosted(ctx context.Context, txn Transaction, entries []Entry) {
	if l.publisher == nil {
		return
	}
	if err := l.publisher.PublishTransactionPosted(ctx, txn, entries); err != nil {
		log.Printf("ledger: failed to publish transaction %s event (transaction already committed; not fatal): %v", txn.ID, err)
	}
}
