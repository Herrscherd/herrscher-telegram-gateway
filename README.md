# herrscher-telegram-gateway

**The Telegram edge.** It adapts the Telegram Bot API to the Herrscher gateway
ports and registers itself from `init()`, so a host enables Telegram with a
blank import and a rebuild. It talks to the Bot API over HTTPS through the
`github.com/Herrscherd/tgctl` client.

It receives updates by long polling (`getUpdates`), so the daemon needs no
public URL and runs fine behind a NAT. It is a smart gateway: it implements
`RoutedEventSink` and draws the live turn itself.

Status: experimental.

## Install

```bash
herrscher plugin add github.com/Herrscherd/herrscher-telegram-gateway
```

Create the bot with BotFather and give the daemon its token. Keep its privacy
mode on, except where a `channels` agent needs to read every message of a group
(see [One bot per agent](#one-bot-per-agent)). The bot must have no webhook:
Telegram refuses to hand out updates by polling while one is set. The gateway
then logs the cause and its liveness probe fails until the webhook is removed
with `deleteWebhook`.

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
repository (the first 20), and the message is delivered once the session exists.
Only the person who asked can pick. With a single repository the session starts
at once.

A message with no text and no usable file (a sticker, a voice note, a location)
is ignored. A file the agent cannot read (not an image, a PDF or a text file, or
over 10 MiB) is not downloaded, and gets one line saying so.

## One bot per agent

An agent can speak through its own Telegram bot. The host lists the agents
of the daemon with their reply rule and their channels, and the gateway
reads that list for each message.

- An agent gets its own bot when the host gives it a token, read from
  `TELEGRAM_BOT_TOKEN_<AGENT>` (the agent name upper-cased, `-` written as `_`).
  A token change needs a restart. Each token signs in once, within 10 seconds,
  and the sign-ins run in parallel, so a token that hangs delays only its own
  agent. A token that cannot sign in is logged without the token, skipped, and
  its agent falls back to the house bot under its header.
- An agent still signing in holds the messages that name it (`@agent` in the
  text, or a `channels` chat of that agent) for at most 10 seconds, the sign-in
  window, and each held message is logged with the agent's name, never its
  token. A token-less agent that the message also names, or that captures the
  chat, answers first, so a ready agent is never held back. The hold applies
  only when the house bot is not explicitly mentioned, and an agent whose reply
  rule is `never` holds nothing. The house never answers a held message under
  that agent's header. Once the sign-in settles, the agent's own bot handles the
  later messages, or, if the sign-in failed, the house answers for that agent
  under its header.
- A text `@agent` that is not a real mention of the agent's bot is not seen by
  that bot under privacy mode, and once the agent has its own bot the house
  never answers it under that agent's header. To reach an agent with its own
  bot, mention its bot (`@ana_bot`).
- Two agents cannot share a bot. An agent whose token is the house bot's, or the
  token of an agent already signed in, is skipped: the first to sign in keeps
  the bot, and the duplicate is logged once without its token and speaks through
  the house under its header.
- An agent with its own bot posts and receives through it, with no header.
  An agent without one speaks through the house bot, and its first line
  names it in bold.
- Two agent names that map to the same env key (`ana-bot` and `ana_bot`) get
  no token from the host, which logs the clash once. Both then speak through
  the house bot under their header.
- A conversation with an agent is addressed `<chat>@<agent>` inside the
  gateway, and its session is named `tg-<chat>[-<thread>]-a-<agent>`, for
  example `tg-n100500-7-a-ana`. A name longer than 64 characters is shortened
  to `tg-a-` and a hash of the address, which stays stable for the same
  conversation and agent.
- Keyboards are always posted by the house bot, under the agent header, even
  for an agent that has its own bot, and their presses are read by the house
  poller. Telegram sends a press to the bot that posted its keyboard, so the
  house posts them all.
- Reply rules: `never` drops every message. `mention` (the default) answers
  when the agent is addressed: its `@username`, a text mention of it, a
  `/command@username`, a reply to one of its messages, or a private chat
  with it. An agent without its own bot also answers to `@agent` written in
  the text. `channels` adds every message of the chats listed for the agent.
  An explicit mention of the house bot is answered alongside a token-less agent,
  as on Discord: the agent named in the text (`@agent`), or else the `channels`
  agent that captures the chat, answers first, then the house. A reply to the
  house is not a mention, so the agent alone answers it.
- A message that mentions an agent bot and not the house bot is not answered
  by the house. A message authored by any of our bots is dropped everywhere.
- Privacy mode decides what a bot sees in a group. With it on, a bot sees only
  commands, mentions and replies. A `channels` agent that has its own bot
  needs privacy mode off for that bot, set with `/setprivacy` in BotFather.
  The house bot needs it off too when a `channels` agent has no token of its
  own, or its token was refused, because the house then reads that group for
  the agent.
- A draft that a user stops is matched on its draft id. The ids are unique
  across the gateway and across restarts because the counter starts from the
  wall clock, which relies on the clock not stepping back, so a stop reaches
  only the turn that streamed it. Two agents without their own bot can speak in
  the same private chat without sharing a stop.

## The live turn

In a private chat the turn streams as a draft (`sendMessageDraft`), updated at
most once a second, with Telegram's stop button on it. The draft is temporary,
so the answer is always posted as a real message at the end, as a reply to the
prompt.

Drafts are private chats only, so in a group the gateway posts one message and
edits it at most every three seconds, then edits it into the final answer.

Each turn answers the message that started it, even when more messages arrive
while it runs: a turn finds its message by author and text. A message the host
drops before its turn starts leaves the queue by the same match when the host
sends its author and text on `abandoned` (added by the herrscher pull request
for issue 206), and by its position otherwise. The prompt gets a 👀 reaction
while its turn runs. It is cleared when the turn completes, swapped for 💔 when
the turn failed, and a failed or stopped turn also says so in its last line. A
prompt that ends before any turn starts, dropped by the host or closed by a
`Done` with no turn open, loses its 👀 the same way. A rate limit from Telegram
(`retry_after`) is waited out.

Answers are formatted with message entities, never with escaped MarkdownV2: the
gateway reads the Markdown a model writes (bold, italic, strikethrough, inline
code, code blocks with their language, links, headings) and sends plain text
with entities counted in UTF-16, split at the 4096 unit limit on line breaks.
An agent's header counts in that limit: each part is measured with the header
on it, so a message never exceeds 4096 units once the header is added.

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
downloaded, and a file name is cut to 120 bytes. The manifest declares no
attachment host, so the host never fetches a Telegram URL itself. A refused file
is explained once per message, by the house bot in the house conversation of the
chat, even when the message reaches several agents.

An album arrives as one message per item. The gateway gathers the items that
share a `media_group_id` until a second passes without a new one, and hands them
over as one message: the caption of the album, and every file in it. A message
sent while an album is gathered waits behind it, so the agent reads them in the
order they were sent. An album still gathering when the daemon stops is lost,
and logged.

Which files the agent can read is decided by `contracts.ModelReadable`, the same
rule the host applies, so the gateway never downloads a file the host drops.

Text the host posts or replies with (a session banner) is Markdown too, and is
sent the same way, with entities, so no marker shows as a literal character. An
underscore inside a word stays literal, so a snake_case name or a path is never
turned into italics. A notice stays plain text, since its arguments are
untrusted.

## Texts

Every text the gateway writes comes from its catalog, in English and French,
following `HERRSCHER_LANG`. A notice is rendered in the recipient's language.

## Development

```bash
scripts/check.sh
```

runs the quality chain: tidy, format, golangci-lint, the comment check, the
tests with the race detector, the build and govulncheck.
