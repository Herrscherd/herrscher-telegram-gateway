# herrscher-telegram-gateway

**The Telegram edge.** It adapts the Telegram Bot API to the Herrscher gateway
ports and registers itself from `init()`, so a host enables Telegram with a blank
import and a rebuild. It talks to the Bot API over plain HTTPS, with no client
library.

It receives updates by long polling (`getUpdates`), so the daemon needs no public
URL and runs fine behind a NAT. It is a smart gateway: it implements
`RoutedEventSink` and draws the live turn itself.

Status: experimental.

## Install

```bash
herrscher plugin add github.com/Herrscherd/herrscher-telegram-gateway
```

Create the bot with BotFather, keep its privacy mode on, and give the daemon its
token. The bot must have no webhook: Telegram refuses to hand out updates by
polling while one is set. The gateway then logs the cause and its liveness
probe fails until the webhook is removed with `deleteWebhook`.

## Configuration

Each setting has a config key and an environment variable. The two are
interchangeable.

| Key | Environment | Default | What it is |
|---|---|---|---|
| `token` | `TELEGRAM_BOT_TOKEN` | **required** | the token BotFather gave |
| `owner` | `TELEGRAM_USER_ID` | | the numeric id of the user the bot always obeys |
| `allowed_users` | `TELEGRAM_ALLOWED_USERS` | | numeric user ids allowed to make the bot act, comma or space separated |
| `allowed_chats` | `TELEGRAM_ALLOWED_CHATS` | none | numeric ids of the groups the bot answers in |
| `allow_all_users` | `TELEGRAM_ALLOW_ALL_USERS` | off | let anyone who can reach the bot make it act |
| `agent` | `TELEGRAM_AGENT` | | the persona every session runs as |

Ports implemented: `Gateway`, `GatewayCloser`, `NoticeSink`, `EventSink`,
`RoutedEventSink`, `SessionControlReceiver`, `Prober`. The plugin also carries a
health probe (`getMe`) the host runs without a live instance.

## Access is refused by default

A Telegram bot can be reached by anyone who finds its name. The gateway refuses
to start unless `owner`, `allowed_users` or `allow_all_users` says who may talk
to it. A message from anyone else, or from another bot, is ignored without an
answer.

A private chat with an allowed user is always open. A group is closed until its
id is in `allowed_chats`, and even then the bot only reads a message that
mentions it (`@name`, or `/command@name`) or that replies to one of its own
messages. With BotFather's privacy mode on, Telegram does not deliver the rest
anyway.

Give the token through `TELEGRAM_BOT_TOKEN` or the private settings file, never
on a command line. The gateway puts it only in the request path to the Bot API.
Every transport error is stripped of it before it reaches a log, and no
attachment URL carrying it is ever handed to the host.

A message refused by the access list is dropped before any work is queued for
it, so a stranger costs the daemon nothing. The stop button of a draft is only
honoured from an allowed user and only for the draft of the turn still running.

## One session per conversation

A private chat is one conversation, a group is one conversation, and each forum
topic of a group is its own conversation. The first message of a conversation
that has no session asks which repository to work on, with one button per
repository (the first 20), and the message is delivered once the session
exists. Only the person who asked can pick. With a single repository the
session starts at once.

A message with no text and no usable file (a sticker, a voice note, a location)
is ignored. A file the agent cannot be handed gets one line saying so.

## The live turn

In a private chat the turn streams as a draft (`sendMessageDraft`), updated at
most once a second, with Telegram's stop button on it. The draft is temporary,
so the answer is always posted as a real message at the end, as a reply to the
prompt.

Drafts are private chats only, so in a group the gateway posts one message and
edits it at most every three seconds, then edits it into the final answer.

Each turn answers the message that started it, even when more messages arrive
while it runs. The prompt gets a 👀 reaction while its turn runs. It is cleared when the turn
completes, swapped for 💔 when the turn failed, and a failed or stopped turn
also says so in its last line. A rate limit from Telegram (`retry_after`) is
waited out.

Answers are formatted with message entities, never with escaped MarkdownV2: the
gateway reads the Markdown a model writes (bold, italic, strikethrough, inline
code, code blocks with their language, links, headings) and sends plain text
with entities counted in UTF-16, split at the 4096 unit limit on line breaks.

## Menus and approvals

`Menu` posts one inline button per choice. A press is handed to the session as a
pick, the buttons are removed, and the press is acknowledged with a short toast.
Telegram caps a button's payload at 64 bytes, so the payload is a random token;
the choice values stay in the gateway.

## Interrupting

`/stop` interrupts the turn running in the conversation. So does the stop button
of a draft: Telegram sends `stopped_message_generation` and the gateway turns it
into an interrupt of the session.

## Reactions

Telegram offers a fixed set of reactions. `React` maps a common emoji to its
nearest offered one (✅ to 👍, ❌ to 👎, ⏳ to 👀) and refuses the others with an
error.

## Attachments

A document or a photo (its largest size) is fetched with `getFile` and staged
under the host's attachment tree (`$TMPDIR/herrscher-attachments/telegram`),
then handed over as a `file://` path. A file over 10 MiB is skipped before it is
downloaded, and a file name is cut to 120 bytes. The manifest declares no attachment host, so the host never fetches
a Telegram URL itself.

Not handled yet: an album arrives as one message per photo, and each one becomes
its own turn. Grouping them by `media_group_id` is the recorded follow-up.

## Texts

Every text the gateway writes comes from its catalog, in English and French,
following `HERRSCHER_LANG`. A notice is rendered in the recipient's language.

## Development

```bash
scripts/check.sh
```

runs the quality chain: tidy, format, golangci-lint, the comment check, the tests
with the race detector, the build and govulncheck.
