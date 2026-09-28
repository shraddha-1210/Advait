# Advait frontend

Four screens over the Advait gateway: **Settlement**, **Netting**, **Security** and **Audit**.
Every balance, rate, status, error code and audit row comes from a live gateway call.
There is no mock layer. If the gateway is down, the app shows a "Gateway offline" banner and disables actions.

## Run

Needs Node.js 20+ and the gateway running (repo README, section 10, steps 1 to 6).

```bash
npm install
npm run dev        # http://localhost:5180
npm run build      # type-check and production build into dist/
npm run lint
```

The gateway URL defaults to `http://localhost:8080`. To point elsewhere, set `VITE_GATEWAY_URL`,
for example in `frontend/.env.local` (git-ignored):

```
VITE_GATEWAY_URL=http://localhost:8080
```

## Layout

- `src/api/types.ts`: response and request shapes. Each type names the Go struct it mirrors.
- `src/api/client.ts`: every fetch. Non-2xx `{"error": ...}` bodies are shown verbatim. A chaincode refusal is a 200 with `outcome.ok = false`, and the UI shows that outcome's code and message.
- `src/screens/`: `Settlement.tsx` (the both-or-neither moment), `Netting.tsx` (gross vs net, one atomic net settlement), `Security.tsx` (attack catalogue, live refusal feed, value conservation), `Audit.tsx` (append-only log, FX provenance, invariant).

## Known limits

- The ledger's list queries (`GetTrades`, `GetAuditLog`) return a capped, unordered subset on the Drunix test network (repo README, section 11). Trades created or opened in the UI are read one by one with `GET /api/trades/{id}`. The audit view sorts by sequence number and warns when entries are missing.
- The refusal feed is the gateway's in-memory log. It resets when the gateway restarts.
- Theme choice is React state only, so it resets on reload.
