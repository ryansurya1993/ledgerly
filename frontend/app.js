'use strict';

/*
 * Ledgerly demo frontend -- plain HTML/CSS/JS, no build step, no
 * framework (see PROJECT_BRIEF.md: this is a backend portfolio piece,
 * the frontend just needs to be clear and demoable).
 *
 * Talks to two origins:
 *  - WALLET_BASE: wallet-service's API, plus a GET /integrity
 *    passthrough to ledger-service (see wallet-service/README.md and
 *    internal/handler/integrity.go) -- this covers every wallet action
 *    and the integrity widget.
 *  - SSE_URL: notification-service's live activity feed. This is the
 *    one genuinely cross-origin call this page makes; it works because
 *    notification-service's /events handler sets
 *    Access-Control-Allow-Origin: * (see its internal/handler/stream.go).
 *
 * This file is served BY wallet-service itself (see cmd/wallet-service's
 * main.go and the root README's frontend section) specifically so every
 * WALLET_BASE call above is same-origin and needs no CORS handling of
 * its own.
 */

const WALLET_BASE = 'http://localhost:8081';
const SSE_URL = 'http://localhost:8082/events';
// Stores only the last *selected* wallet's id, as a reload convenience
// -- not the list of wallets that exist. See fetchWallets/walletsById
// below for why that list itself is never persisted client-side.
const LAST_WALLET_STORAGE_KEY = 'ledgerly_last_wallet_id';
const SINK_STORAGE_KEY = 'ledgerly_stress_sink_id';

// The one account that exists in every ledger even before any wallet
// is ever created: ledger-service's migration 000001 seeds it up
// front as the fixed debit side of every top-up (see
// ledger-service/internal/ledger/types.go's ExternalFundingAccountID
// doc comment -- wallet-service's own Go code already treats this
// exact ID as a public, stable constant the same way). GET /integrity
// -- correctly -- includes it in every response, since it's a real
// ledger account like any other; this frontend excludes it from the
// *displayed* count below so "Books balance (N wallets)" means what a
// visitor actually expects it to mean, rather than reading "1" on a
// freshly reset database that has zero visitor-created wallets.
const EXTERNAL_FUNDING_ACCOUNT_ID = '00000000-0000-0000-0000-000000000001';

// ---------------------------------------------------------------------
// State
// ---------------------------------------------------------------------

// walletsById is populated only by fetchWallets(), from wallet-service's
// GET /wallets -- Postgres (via that endpoint) is the source of truth
// for which wallets exist, per CLAUDE.md's statelessness principle, so
// this is never written to localStorage and never mutated by hand.
// { [id]: {id, name, currency, balance, created_at, updated_at} }
let walletsById = {};
let selectedWalletId = null;
let lastTransaction = null;        // see enableReplayLastButton's callers

function loadLastWalletId() {
  try { return localStorage.getItem(LAST_WALLET_STORAGE_KEY) || null; }
  catch { return null; }
}
function saveLastWalletId(id) {
  try { localStorage.setItem(LAST_WALLET_STORAGE_KEY, id); }
  catch { /* localStorage unavailable (private mode etc.) -- non-fatal, just no restore-on-reload */ }
}

// ---------------------------------------------------------------------
// Small utilities
// ---------------------------------------------------------------------

function dollarsToCents(value) {
  const cents = Math.round(parseFloat(value) * 100);
  if (!Number.isFinite(cents) || cents <= 0) {
    throw new Error('Enter a positive dollar amount.');
  }
  return cents;
}

function centsToDollars(cents) {
  return (cents / 100).toLocaleString('en-US', { style: 'currency', currency: 'USD' });
}

function newIdempotencyKey() {
  return crypto.randomUUID ? crypto.randomUUID() : `${Date.now()}-${Math.random()}`;
}

function short(id) {
  return typeof id === 'string' ? id.slice(0, 8) + '…' : String(id);
}

function formatTime(iso) {
  try { return new Date(iso).toLocaleTimeString(); }
  catch { return iso; }
}

function escapeHtml(s) {
  const div = document.createElement('div');
  div.textContent = s == null ? '' : String(s);
  return div.innerHTML;
}

function clampInt(value, min, max, fallback) {
  const n = parseInt(value, 10);
  if (!Number.isFinite(n)) return fallback;
  return Math.min(max, Math.max(min, n));
}

function show(el) { el.hidden = false; }
function hide(el) { el.hidden = true; }

function showFieldError(el, message) {
  el.textContent = message;
  show(el);
}

// ---------------------------------------------------------------------
// wallet-service API client
// ---------------------------------------------------------------------

// api() throws on any non-2xx response, with .message set to the
// server's own {"error": "..."} text when available -- callers show
// err.message directly rather than a raw JSON dump (see the "Try to
// overdraft" test below, which is exactly what this is for).
async function api(method, path, body) {
  const res = await fetch(WALLET_BASE + path, {
    method,
    headers: body !== undefined ? { 'Content-Type': 'application/json' } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });

  let data = null;
  try { data = await res.json(); } catch { /* no/invalid JSON body */ }

  if (!res.ok) {
    const message = (data && data.error) ? data.error : `Request failed (HTTP ${res.status})`;
    const err = new Error(message);
    err.status = res.status;
    throw err;
  }
  return data;
}

// ---------------------------------------------------------------------
// Wallet panel
// ---------------------------------------------------------------------

// fetchWallets is the only place walletsById is ever populated, from
// wallet-service's GET /wallets (a passthrough to ledger-service's own
// list of every real wallet account). This -- not localStorage -- is
// the source of truth for which wallets exist: a wallet that no longer
// exists (e.g. after `docker-compose down -v` resets the backend's
// database) simply stops appearing in the array this returns, so
// there's nothing stale left client-side to detect or clean up, unlike
// the old localStorage-as-source-of-truth design this replaces.
async function fetchWallets() {
  const list = await api('GET', '/wallets');
  walletsById = {};
  for (const w of list) walletsById[w.id] = w;
  return list;
}

function renderWalletSelect() {
  const sel = document.getElementById('walletSelect');
  const ids = Object.keys(walletsById);
  sel.innerHTML = '';

  if (ids.length === 0) {
    sel.appendChild(new Option('— none yet —', ''));
    return;
  }
  sel.appendChild(new Option('Choose…', ''));
  for (const id of ids) {
    const opt = new Option(`${walletsById[id].name} (${short(id)})`, id);
    opt.selected = id === selectedWalletId;
    sel.appendChild(opt);
  }
}

// renderTransferDestSelect populates the transfer form's destination
// dropdown from known wallets, excluding whichever one is currently
// selected (the sender) -- a wallet can't transfer to itself, and
// ledger-service's own leg-netting would otherwise turn that into a
// confusing silent no-op rather than a clear error (see
// wallet-service's Transfer handler doc comment). Using a dropdown of
// already-known wallet IDs here, rather than a free-text field, is
// also what fixed a real bug: a manually-typed destination could
// easily be a malformed UUID (400: "invalid destination_wallet_id"),
// or -- very easy to do by accident when the source wallet's own ID is
// displayed right above this form -- literally the sender's own ID
// (400: "destination_wallet_id must differ from the source wallet").
// Both are now impossible to submit, since both would already exclude
// or invalidate the option before it's ever selectable.
function renderTransferDestSelect() {
  const sel = document.getElementById('transferDest');
  const priorValue = sel.value;
  sel.innerHTML = '';

  const otherIds = Object.keys(walletsById).filter((id) => id !== selectedWalletId);
  if (otherIds.length === 0) {
    const placeholder = new Option('— no other wallets yet —', '', true, true);
    placeholder.disabled = true;
    sel.appendChild(placeholder);
    sel.disabled = true;
    return;
  }

  sel.disabled = false;
  const placeholder = new Option('Choose a destination wallet…', '', true, true);
  placeholder.disabled = true;
  sel.appendChild(placeholder);
  for (const id of otherIds) {
    // Full ID, not short(id)'s truncated form: this dropdown's open
    // list renders below the control with room for it (unlike the
    // walletSelect dropdown above, which stays deliberately short so
    // the ID doesn't matter as much there), and seeing the whole ID is
    // what actually lets a visitor tell two similarly-named wallets
    // apart with confidence before sending money to one of them.
    const opt = new Option(`${walletsById[id].name} (${id})`, id);
    opt.selected = id === priorValue;
    sel.appendChild(opt);
  }
}

// selectWallet is the single place every "make this wallet the active
// one" path goes through (creating one, loading one by ID, restoring
// the last-selected one on page load, or picking one from
// walletSelect's dropdown). Every caller passes an id already confirmed
// against a freshly-fetched walletsById -- unlike the old
// localStorage-as-source-of-truth design, there's no "the browser
// remembers an id Postgres has since forgotten" case to recover from
// here anymore: an id that no longer exists (e.g. after
// `docker-compose down -v` resets the backend's database) simply isn't
// present in walletsById in the first place, since that's rebuilt from
// wallet-service's GET /wallets on every fetchWallets() call -- so it
// was never offered as a selectable option to begin with. The try/catch
// below is only for genuine transient failures (wallet-service
// momentarily unreachable, a network blip), not stale-ID recovery.
async function selectWallet(id) {
  selectedWalletId = id;
  saveLastWalletId(id);
  renderWalletSelect();
  renderTransferDestSelect();
  clearActionMessages();
  hide(document.getElementById('loadWalletError'));

  try {
    await Promise.all([refreshBalance(), refreshHistory()]);
  } catch (err) {
    showFieldError(document.getElementById('loadWalletError'), `Couldn't load this wallet: ${err.message}`);
    return;
  }

  show(document.getElementById('selectedWalletPanel'));
  document.getElementById('selectedWalletName').textContent = (walletsById[id] && walletsById[id].name) || 'Wallet';
  document.getElementById('selectedWalletId').textContent = id;
}

async function refreshBalance() {
  if (!selectedWalletId) return 0;
  const balance = await api('GET', `/wallets/${encodeURIComponent(selectedWalletId)}/balance`);
  document.getElementById('selectedWalletBalance').textContent = centsToDollars(balance.balance);
  return balance.balance;
}

async function refreshHistory() {
  if (!selectedWalletId) return;
  const history = await api('GET', `/wallets/${encodeURIComponent(selectedWalletId)}/history`);
  const tbody = document.getElementById('historyBody');
  tbody.innerHTML = '';

  if (!history.entries || history.entries.length === 0) {
    tbody.innerHTML = '<tr><td colspan="5" class="muted">No entries yet.</td></tr>';
    return;
  }

  // API returns oldest-first; show newest-first for a live feel.
  for (const entry of [...history.entries].reverse()) {
    const dirClass = entry.direction === 'credit' ? 'dir-credit' : 'dir-debit';
    const sign = entry.direction === 'credit' ? '+' : '−';
    const tr = document.createElement('tr');
    tr.innerHTML = `
      <td>${formatTime(entry.created_at)}</td>
      <td>${escapeHtml(entry.transaction_type)}</td>
      <td class="${dirClass}">${escapeHtml(entry.direction)}</td>
      <td class="${dirClass}">${sign}${centsToDollars(entry.amount)}</td>
      <td class="desc">${escapeHtml(entry.description || '—')}</td>
    `;
    tbody.appendChild(tr);
  }
}

function clearActionMessages() {
  hide(document.getElementById('actionResult'));
  hide(document.getElementById('actionSuccess'));
}
function showActionError(message) { showFieldError(document.getElementById('actionResult'), message); }
function showActionSuccess(message) {
  const el = document.getElementById('actionSuccess');
  el.textContent = message;
  show(el);
}

function enableReplayLastButton() {
  document.getElementById('replayLastBtn').disabled = false;
}

document.getElementById('createWalletForm').addEventListener('submit', async (e) => {
  e.preventDefault();
  const errEl = document.getElementById('createWalletError');
  hide(errEl);
  const nameInput = document.getElementById('newWalletName');
  const name = nameInput.value.trim();
  if (!name) return;

  try {
    const wallet = await api('POST', '/wallets', { name });
    await fetchWallets();
    nameInput.value = '';
    await selectWallet(wallet.id);
  } catch (err) {
    showFieldError(errEl, err.message);
  }
});

document.getElementById('walletSelect').addEventListener('change', (e) => {
  if (e.target.value) selectWallet(e.target.value);
});

// Loading a pasted wallet ID re-fetches the wallet list first (rather
// than, say, hitting GET /wallets/{id}/balance to check existence) so
// this goes through the exact same source-of-truth check as everything
// else here -- one round trip either confirms the id and populates
// walletsById for the render that follows, or it doesn't, and that's a
// plain "no such wallet" the visitor can act on (most likely a typo:
// this is a hand-typed ID, not one recalled from localStorage, so
// there's no staleness story to tell here, just "check what you typed").
document.getElementById('loadWalletBtn').addEventListener('click', async () => {
  const errEl = document.getElementById('loadWalletError');
  hide(errEl);
  const input = document.getElementById('loadWalletId');
  const id = input.value.trim();
  if (!id) return;

  try {
    await fetchWallets();
    if (!walletsById[id]) {
      throw new Error(`No wallet with that ID exists (${short(id)}).`);
    }
    input.value = '';
    await selectWallet(id);
  } catch (err) {
    showFieldError(errEl, err.message);
  }
});

document.getElementById('topupForm').addEventListener('submit', async (e) => {
  e.preventDefault();
  clearActionMessages();
  try {
    const amountCents = dollarsToCents(document.getElementById('topupAmount').value);
    const idempotencyKey = newIdempotencyKey();
    const description = 'Manual top-up';
    const resp = await api('POST', `/wallets/${encodeURIComponent(selectedWalletId)}/topup`, {
      idempotency_key: idempotencyKey,
      amount: amountCents,
      description,
    });
    lastTransaction = { kind: 'topup', idempotencyKey, walletId: selectedWalletId, amountCents, description };
    enableReplayLastButton();
    showActionSuccess(`Transaction ${resp.transaction_id}.\nTopped up ${centsToDollars(amountCents)}.`);
    await Promise.all([refreshBalance(), refreshHistory()]);
  } catch (err) {
    showActionError(err.message);
  }
});

document.getElementById('transferForm').addEventListener('submit', async (e) => {
  e.preventDefault();
  clearActionMessages();
  try {
    const destWalletId = document.getElementById('transferDest').value;
    if (!destWalletId) throw new Error('Create another wallet first, then choose it as the destination.');
    const amountCents = dollarsToCents(document.getElementById('transferAmount').value);
    const idempotencyKey = newIdempotencyKey();
    const resp = await api('POST', `/wallets/${encodeURIComponent(selectedWalletId)}/transfer`, {
      idempotency_key: idempotencyKey,
      destination_wallet_id: destWalletId,
      amount: amountCents,
    });
    lastTransaction = { kind: 'transfer', idempotencyKey, walletId: selectedWalletId, destWalletId, amountCents };
    enableReplayLastButton();
    showActionSuccess(`Transaction ${resp.transaction_id}.\nTransferred ${centsToDollars(amountCents)} to ${destWalletId}.`);
    await Promise.all([refreshBalance(), refreshHistory()]);
  } catch (err) {
    showActionError(err.message);
  }
});

// ---------------------------------------------------------------------
// Stress-test panel
// ---------------------------------------------------------------------

function requireWallet(box) {
  if (!selectedWalletId) {
    show(box);
    box.className = 'result-box bad';
    box.textContent = 'Select or create a wallet first.';
    return false;
  }
  return true;
}

// summarizeOutcomes turns a list of {ok: true} / {ok: false, error}
// results from a batch of fired requests into one human-readable line
// like "18 succeeded, 2 rejected: insufficient funds" -- grouping
// failures by their message so a mix of causes stays readable (e.g.
// "17 succeeded, 2 rejected: insufficient funds, 1 rejected: Request
// failed (HTTP 503)"). Shown on-page instead of only logging to the
// console: a visitor running the stress-test buttons shouldn't have to
// open devtools to find out *why* a count didn't match what they
// expected.
function summarizeOutcomes(outcomes) {
  const failures = outcomes.filter((o) => !o.ok);
  const succeededCount = outcomes.length - failures.length;
  if (failures.length === 0) {
    return `${succeededCount} succeeded`;
  }

  const countByMessage = new Map();
  for (const failure of failures) {
    countByMessage.set(failure.error, (countByMessage.get(failure.error) || 0) + 1);
  }
  const failureParts = Array.from(countByMessage.entries())
    .map(([message, count]) => `${count} rejected: ${message}`);
  return [`${succeededCount} succeeded`, ...failureParts].join(', ');
}

// Every test below that needs a transfer destination shares one
// "sink" wallet, created once and cached in localStorage, rather than
// making the visitor create a second wallet just to run a stress test.
async function ensureStressSinkWallet() {
  const cachedId = localStorage.getItem(SINK_STORAGE_KEY);
  if (cachedId) {
    try {
      await api('GET', `/wallets/${encodeURIComponent(cachedId)}/balance`);
      return cachedId;
    } catch { /* cached wallet no longer exists (fresh DB) -- create a new one */ }
  }
  const wallet = await api('POST', '/wallets', { name: 'Stress-test sink' });
  localStorage.setItem(SINK_STORAGE_KEY, wallet.id);
  return wallet.id;
}

// (a) Fire N idempotent replays -- proves idempotency: N racing
// requests with the SAME key must collapse into exactly one
// transaction and exactly one balance change, no matter how many of
// them "win" the race to arrive first.
document.getElementById('fireReplaysBtn').addEventListener('click', async () => {
  const box = document.getElementById('replayResult');
  if (!requireWallet(box)) return;

  const n = clampInt(document.getElementById('replayN').value, 2, 200, 20);
  const amountCents = 1000; // fixed $10.00 -- kept constant so the "expect +$10.00 total" line below is simple to read
  const key = newIdempotencyKey();

  show(box);
  box.className = 'result-box';
  box.textContent = `Firing ${n} simultaneous top-ups, all with the same idempotency key…`;

  const before = await refreshBalance();
  const outcomes = await Promise.all(Array.from({ length: n }, () =>
    api('POST', `/wallets/${encodeURIComponent(selectedWalletId)}/topup`, {
      idempotency_key: key,
      amount: amountCents,
      description: 'Idempotency stress test',
    }).then(r => ({ ok: true, transactionId: r.transaction_id })).catch(e => ({ ok: false, error: e.message }))
  ));
  const succeeded = outcomes.filter(o => o.ok);
  const uniqueIds = new Set(succeeded.map(o => o.transactionId));
  const after = await refreshBalance();
  await refreshHistory();

  const balanceDelta = after - before;
  const passed = uniqueIds.size === 1 && balanceDelta === amountCents;

  box.className = 'result-box ' + (passed ? 'ok' : 'bad');
  box.innerHTML = `
    <div class="result-line"><span>Requests fired</span><strong>${n}</strong></div>
    <div class="result-line"><span>Outcome</span><strong>${escapeHtml(summarizeOutcomes(outcomes))}</strong></div>
    <div class="result-line"><span>Unique transaction IDs returned</span><strong>${uniqueIds.size}</strong> <span class="muted">(expect 1)</span></div>
    <div class="result-line"><span>Balance change</span><strong>${centsToDollars(balanceDelta)}</strong> <span class="muted">(expect ${centsToDollars(amountCents)} -- applied exactly once)</span></div>
    <div class="verdict">${passed
      ? '✓ Idempotency held: one key in, one transaction out, regardless of how many requests raced for it.'
      : '✗ Unexpected: more than one transaction or balance change was recorded.'}</div>
  `;
});

// (b) Fire N concurrent transfers -- proves optimistic-concurrency
// correctness: N racing requests with DIFFERENT keys must ALL apply,
// with no lost updates, even though they're all updating the same
// source account's balance at once.
document.getElementById('fireConcurrentBtn').addEventListener('click', async () => {
  const box = document.getElementById('concurrentResult');
  if (!requireWallet(box)) return;

  const n = clampInt(document.getElementById('concurrentN').value, 2, 200, 20);
  let amountCents;
  try {
    amountCents = dollarsToCents(document.getElementById('concurrentAmount').value);
  } catch (err) {
    show(box);
    box.className = 'result-box bad';
    box.textContent = err.message;
    return;
  }

  show(box);
  box.className = 'result-box';
  box.textContent = 'Preparing sink wallet…';

  try {
    const sinkId = await ensureStressSinkWallet();
    const totalNeeded = amountCents * n;
    const before = await refreshBalance();

    // Fail clearly, upfront, rather than silently topping up the
    // wallet on the visitor's behalf: an auto-funding transaction
    // showing up in the wallet's own history is more confusing than
    // helpful, and this test is about proving concurrency correctness
    // on whatever balance the wallet actually has -- not about
    // guaranteeing it can always run regardless of that balance.
    if (before < totalNeeded) {
      box.className = 'result-box bad';
      box.innerHTML = `
        <div class="result-line"><span>Balance needed (N × amount)</span><strong>${centsToDollars(totalNeeded)}</strong></div>
        <div class="result-line"><span>Current balance</span><strong>${centsToDollars(before)}</strong></div>
        <div class="verdict">✗ Insufficient balance to run this test -- top up the wallet (or lower N / the amount) and try again.</div>
      `;
      return;
    }

    box.textContent = `Firing ${n} simultaneous transfers of ${centsToDollars(amountCents)} each…`;

    const outcomes = await Promise.all(Array.from({ length: n }, () =>
      api('POST', `/wallets/${encodeURIComponent(selectedWalletId)}/transfer`, {
        idempotency_key: newIdempotencyKey(),
        destination_wallet_id: sinkId,
        amount: amountCents,
      }).then(() => ({ ok: true })).catch(e => ({ ok: false, error: e.message }))
    ));
    const succeeded = outcomes.filter(o => o.ok).length;
    const failed = outcomes.length - succeeded;

    const after = await refreshBalance();
    await refreshHistory();

    const expected = before - amountCents * n;
    const passed = failed === 0 && after === expected;

    let verdict;
    if (passed) {
      verdict = '✓ Every concurrent transfer applied exactly once -- no lost updates.';
    } else if (failed > 0) {
      verdict = '✗ Not every transfer succeeded -- see the outcome breakdown above.';
    } else {
      verdict = "✗ Unexpected: every transfer reported success, but the final balance doesn't match -- open the console for per-request details.";
    }

    box.className = 'result-box ' + (passed ? 'ok' : 'bad');
    box.innerHTML = `
      <div class="result-line"><span>Transfers fired</span><strong>${n}</strong></div>
      <div class="result-line"><span>Outcome</span><strong>${escapeHtml(summarizeOutcomes(outcomes))}</strong></div>
      <div class="result-line"><span>Balance before</span><strong>${centsToDollars(before)}</strong></div>
      <div class="result-line"><span>Expected after (before − N×amount)</span><strong>${centsToDollars(expected)}</strong></div>
      <div class="result-line"><span>Actual after</span><strong>${centsToDollars(after)}</strong></div>
      <div class="verdict">${verdict}</div>
    `;
    if (!passed) console.warn('Concurrent transfer outcomes:', outcomes);
  } catch (err) {
    box.className = 'result-box bad';
    box.textContent = 'Error: ' + err.message;
  }
});

// (c) Try to overdraft -- proves the ledger cleanly rejects a transfer
// it can't cover, with a real error, not a partially-applied balance.
document.getElementById('overdraftBtn').addEventListener('click', async () => {
  const box = document.getElementById('overdraftResult');
  if (!requireWallet(box)) return;

  show(box);
  box.className = 'result-box';
  box.textContent = 'Attempting a transfer larger than the current balance…';

  try {
    const sinkId = await ensureStressSinkWallet();
    const balance = await refreshBalance();
    const overAmount = balance + 100000; // current balance + $1,000 -- guaranteed to overdraft
    await api('POST', `/wallets/${encodeURIComponent(selectedWalletId)}/transfer`, {
      idempotency_key: newIdempotencyKey(),
      destination_wallet_id: sinkId,
      amount: overAmount,
    });
    box.className = 'result-box bad';
    box.textContent = '✗ Unexpected: the oversized transfer was NOT rejected.';
  } catch (err) {
    const correct = err.status === 409;
    box.className = 'result-box ' + (correct ? 'ok' : 'bad');
    box.innerHTML = `<div class="verdict">${correct ? '✓ Correctly rejected' : `Rejected, but with HTTP ${err.status} instead of the expected 409`}:</div> ${escapeHtml(err.message)}`;
  }
  await refreshBalance();
});

// (d) Replay last transaction -- resubmits whatever top-up/transfer
// this session last made, with its exact original idempotency key.
document.getElementById('replayLastBtn').addEventListener('click', async () => {
  const box = document.getElementById('replayLastResult');
  show(box);
  box.className = 'result-box';
  box.textContent = 'Replaying…';

  try {
    let resp;
    if (lastTransaction.kind === 'topup') {
      resp = await api('POST', `/wallets/${encodeURIComponent(lastTransaction.walletId)}/topup`, {
        idempotency_key: lastTransaction.idempotencyKey,
        amount: lastTransaction.amountCents,
        description: lastTransaction.description,
      });
    } else {
      resp = await api('POST', `/wallets/${encodeURIComponent(lastTransaction.walletId)}/transfer`, {
        idempotency_key: lastTransaction.idempotencyKey,
        destination_wallet_id: lastTransaction.destWalletId,
        amount: lastTransaction.amountCents,
      });
    }
    box.className = 'result-box ' + (resp.replayed ? 'ok' : 'bad');
    box.innerHTML = `Server reports <strong>replayed: ${resp.replayed}</strong> for transaction ${resp.transaction_id} — ${resp.replayed
      ? 'the exact same transaction was returned, no new money moved.'
      : 'unexpected: a NEW transaction was created from a reused key.'}`;
    if (lastTransaction.walletId === selectedWalletId) {
      await Promise.all([refreshBalance(), refreshHistory()]);
    }
  } catch (err) {
    box.className = 'result-box bad';
    box.textContent = 'Error: ' + err.message;
  }
});

// (e) Live integrity check -- polls the system-wide drift check and
// shows a simple green/red indicator.
async function pollIntegrity() {
  const dot = document.getElementById('integrityDot');
  const text = document.getElementById('integrityText');
  try {
    const result = await api('GET', '/integrity');
    if (result.drifted) {
      dot.className = 'dot dot-bad';
      text.textContent = 'DRIFT DETECTED';
    } else {
      const walletCount = result.results.filter((r) => r.account_id !== EXTERNAL_FUNDING_ACCOUNT_ID).length;
      dot.className = 'dot dot-ok';
      text.textContent = `Books balance (${walletCount} wallets)`;
    }
  } catch {
    dot.className = 'dot dot-bad';
    text.textContent = 'unreachable';
  }
}

// ---------------------------------------------------------------------
// Live activity feed (SSE from notification-service)
// ---------------------------------------------------------------------

const FEED_MAX_LINES = 100;

function connectActivityFeed() {
  const dot = document.getElementById('sseDot');
  const text = document.getElementById('sseText');

  // EventSource reconnects automatically on its own after a drop --
  // see notification-service/README.md -- so there's no manual retry
  // logic needed here, just status reporting.
  const source = new EventSource(SSE_URL);

  source.onopen = () => {
    dot.className = 'dot dot-ok';
    text.textContent = 'live';
  };
  source.onerror = () => {
    dot.className = 'dot dot-bad';
    text.textContent = 'reconnecting…';
  };

  // notification-service publishes this as a named SSE event (not the
  // default "message" event), so it's caught here, not via onmessage.
  source.addEventListener('transaction.posted', (event) => {
    let evt;
    try { evt = JSON.parse(event.data); } catch { return; }
    appendFeedLine(evt);
  });
}

function appendFeedLine(evt) {
  const feed = document.getElementById('activityFeed');
  const empty = feed.querySelector('.feed-empty');
  if (empty) empty.remove();

  const legs = (evt.legs || []).map(leg => {
    const cls = leg.direction === 'credit' ? 'feed-amount-credit' : 'feed-amount-debit';
    const sign = leg.direction === 'credit' ? '+' : '−';
    return `<span class="${cls}">${sign}${centsToDollars(leg.amount)} → ${leg.account_id}</span>`;
  }).join('  ');

  const line = document.createElement('div');
  line.className = 'feed-line';
  line.innerHTML = `
    <span class="feed-time">${formatTime(evt.created_at)}</span>
    <span class="feed-type">${escapeHtml(evt.transaction_type)}</span>
    <span class="feed-txid">txn ${evt.transaction_id}</span>
    <span class="feed-detail">${legs}</span>
  `;
  feed.insertBefore(line, feed.firstChild);

  while (feed.children.length > FEED_MAX_LINES) {
    feed.removeChild(feed.lastChild);
  }
}

// ---------------------------------------------------------------------
// Init
// ---------------------------------------------------------------------

// initWallets loads the real wallet list before doing anything else
// that depends on it, then -- as a pure reload convenience, not a
// source of truth -- restores whichever wallet was last selected, but
// only if it's actually still in that fetched list; loadLastWalletId()
// returning a now-nonexistent id (backend database reset since the
// last visit) just quietly doesn't restore anything, leaving the
// normal "no wallet selected" state, rather than needing dedicated
// stale-ID error handling the way selecting one used to.
async function initWallets() {
  try {
    await fetchWallets();
  } catch (err) {
    console.error('Failed to load wallet list:', err);
  }
  renderWalletSelect();
  renderTransferDestSelect();

  const lastId = loadLastWalletId();
  if (lastId && walletsById[lastId]) {
    await selectWallet(lastId);
  }
}

initWallets();
pollIntegrity();
setInterval(pollIntegrity, 4000);
connectActivityFeed();
