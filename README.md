# tsuke

A self-hosted Telegram bot for entering transactions into [Firefly III](https://www.firefly-iii.org/) by hand: quickly, with a one-line syntax, a draft card you can tweak, and receipt photos attached.

Built for a single user with a low volume of transactions. Go, pure-Go dependencies (no CGO), SQLite for state.

<!-- Screenshots: add images to docs/ and link them here -->
> _Screenshots: draft card · date picker · saved card (placeholder)_

## Features

- **One-line entry**: `350 кофе`, `+50000 зарплата`, `5000 тбанк > наличные`.
- **Draft card**: every field is editable with inline buttons; the card is edited in place instead of spamming new messages. Save / Cancel at the bottom.
- **Guided mode** (`/new`) for when you do not want to type syntax.
- **Pickers** for accounts, categories, budgets and tags: cached from the API, most used first, paginated, and searchable by typing.
- **Date and time** that are quick to change: Now / Yesterday / 2 days ago, an inline calendar, time presets and a ±hour / ±15 min stepper, or just type `вчера 9:15`. Sent to Firefly as ISO 8601 with the right offset.
- **Receipts**: photos, image documents and PDFs (single or album). The Russian fiscal QR code (`t=…&s=…&fn=…&i=…&fp=…&n=…`) prefills amount, date and type, and its `fn/i/fp` are stored in the notes. Files are uploaded as Firefly attachments.
- **Saved card** with Edit, Delete (with confirmation) and a link to the transaction in the Firefly UI.
- Allowlist only: everyone else gets no reply at all.
- Bot messages are in Russian, kept in one string table (`internal/i18n`) so another language can be added.

## Setup

### 1. Create the bot

1. Talk to [@BotFather](https://t.me/BotFather), send `/newbot` and copy the token.
2. Find your numeric Telegram user id (for example with [@userinfobot](https://t.me/userinfobot)). Only ids listed in `ALLOWED_USER_IDS` can use the bot.

The bot registers its command list itself (`setMyCommands`) on start.

### 2. Firefly III token

In Firefly III open **Options → Profile → OAuth → Personal Access Tokens**, create a token and copy it. The bot needs permission to read accounts, categories, budgets and tags, and to create, update and delete transactions and attachments.

### 3. Configure

```fish
cp .env.example .env
$EDITOR .env
```

| Variable | Required | Description |
| --- | --- | --- |
| `TELEGRAM_BOT_TOKEN` | yes | Token from BotFather. |
| `FIREFLY_URL` | yes | Firefly III base URL, e.g. `https://firefly.example.com`. |
| `FIREFLY_TOKEN` | yes | Personal access token. |
| `ALLOWED_USER_IDS` | yes | Comma-separated Telegram user ids. |
| `TZ` | no | IANA time zone, default `Europe/Moscow`. |
| `DEFAULT_SOURCE_ACCOUNT` | no | Name or id of the asset account used when none is given. Falls back to the most used one. |
| `DATA_DIR` | no | Where SQLite and draft files live, default `/data`. |
| `LOG_LEVEL` | no | `debug`, `info`, `warn`, `error`; default `info`. |
| `FIREFLY_PUBLIC_URL` | no | URL used for links in messages if `FIREFLY_URL` is an internal address. |
| `CACHE_REFRESH_HOURS` | no | How often accounts and categories are reloaded, default `6`. |
| `WEBHOOK_URL`, `WEBHOOK_SECRET`, `WEBHOOK_LISTEN`, `WEBHOOK_PATH` | no | Webhook mode, see below. |

### 4. Run with Docker Compose

The container stores its data in `/data`; `docker-compose.yml` bind-mounts `./data` there. The image runs as uid/gid `65532`, so make the directory writable for it:

```fish
mkdir -p data
sudo chown 65532:65532 data
docker compose up -d --build
docker compose logs -f
```

Back up by copying `./data` (it holds `tsuke.db` and drafts in progress).

### Long polling vs webhook

Long polling is the default and needs no inbound connectivity. For a webhook, put the bot behind HTTPS (a reverse proxy) and set:

```
WEBHOOK_URL=https://bot.example.com
WEBHOOK_SECRET=some-long-random-string
# optional: WEBHOOK_LISTEN=:8080  WEBHOOK_PATH=/telegram/webhook
```

The bot registers `WEBHOOK_URL + WEBHOOK_PATH` with Telegram and verifies the secret header on every request. Uncomment `ports` in the compose file to expose the listener.

## Input syntax

The first amount in the message becomes the transaction amount. Everything the parser does not recognise goes to the description.

| Input | Result |
| --- | --- |
| `350 кофе` | withdrawal, 350, description "кофе", default account |
| `+50000 зарплата` | deposit |
| `5000 тбанк > наличные` | transfer between asset accounts (`>`, `->` and `→` all work) |
| `350 кофе #еда @тбанк !работа` | category, account and tag |
| `350 кофе вчера`, `позавчера`, `12.10`, `12.10 14:30`, `14:30`, `вчера 9:15` | date and/or time (the missing part is "now") |
| `1 500`, `1500,50`, `1.5к` | amounts |

Notes:

- Spaces inside a name are written with `_`: `#еда_вне_дома`.
- Accounts, categories and tags are matched fuzzily: case-insensitive, prefix, word prefix, substring, ignoring `-` and spaces and treating `ё` as `е`. So `тбанк` finds "Т-Банк". An unknown category or tag is created by Firefly under the typed name.
- For withdrawals the destination is a free-text payee (Firefly creates the expense account); for deposits the source is a free-text revenue account. Pick them with the **Куда** / **Откуда** buttons.
- `12.10` alone is a date; it is read as an amount only when there is no other number.

## Commands

| Command | What it does |
| --- | --- |
| `/start`, `/help` | Greeting and syntax help. |
| `/new` | Guided step-by-step entry. |
| `/last [n]` | Latest `n` transactions (default 5, max 10) with buttons that open their cards. |
| `/undo` | Delete the last transaction created by the bot, after a confirmation. |
| `/balance` | Balances of active asset accounts. |
| `/today`, `/week`, `/month` | Spending totals by category (weeks start on Monday). |
| `/refresh` | Reload accounts, categories, budgets and tags from Firefly now. |
| `/cancel` | Discard the current draft. |

## Receipts

Send a photo, an image document or a PDF, alone or as an album (an album becomes one draft). An optional caption is parsed like a normal message (`350 кофе #еда`).

- If a Russian fiscal QR code is found, the amount, date and time are prefilled. `n=1` (purchase) makes a withdrawal; `n=2` (return) makes a deposit. `fn`, `i` and `fp` are appended to the notes (`Чек: fn=… i=… fp=…`).
- If no QR code is found, the files are kept and the bot asks for the amount.
- On save, every file is uploaded as a Firefly attachment of the transaction journal.
- **Reply** to a saved card with a photo to attach it to that existing transaction.
- Use the **Файл** button on a draft to add more files to it.
- For PDFs the QR search looks at embedded JPEG images only (typical for scans and exported photos); other PDFs are kept as attachments without prefilling.

Fetching line items from the tax service is out of scope for v1. `receipt.Enricher` in `internal/receipt` is the extension point for it.

## Reliability notes

- Transient network errors and 5xx answers are retried. Before every retry of a create, the bot looks the transaction up by its `external_id`, so a request that Firefly committed but whose answer was lost is never created twice. Requests also carry `error_if_duplicate_hash: true`.
- Firefly validation errors are shown on the card in readable form; the draft is kept so you can fix it and press Save again.
- Drafts live in SQLite and survive restarts; untouched drafts are removed after 7 days.
- Secrets (bot token, Firefly token, webhook secret) are masked in logs.

## Development

Requires Go 1.26 or newer.

```fish
go test -race ./...
golangci-lint run ./...
go run ./cmd/tsuke    # reads configuration from the environment
```

Layout:

```
cmd/tsuke          entry point
internal/config    environment configuration
internal/bot       Telegram handlers, cards, pickers, calendar, commands
internal/firefly   Firefly III API client
internal/parse     quick-entry, amount and date/time parsing, fuzzy matching
internal/receipt   fiscal QR parsing and QR decoding (pure Go, gozxing)
internal/store     SQLite state (modernc.org/sqlite, no CGO)
internal/i18n      user-facing strings
internal/logx      slog setup with secret masking
```

Libraries: [`go-telegram/bot`](https://github.com/go-telegram/bot) (actively maintained, tracks the current Bot API, small API surface, supports webhooks), [`gozxing`](https://github.com/makiuchi-d/gozxing) for QR decoding and [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite) for storage, so the whole thing builds with `CGO_ENABLED=0` for amd64 and arm64.

CI (GitHub Actions) runs lint, tests and a multi-arch Docker build on every pull request.

## License

See [LICENSE](LICENSE).
