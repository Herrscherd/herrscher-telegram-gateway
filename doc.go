// Package telegram is the Telegram edge of Herrscher. It adapts the Telegram Bot
// API to the gateway ports of the contracts module and registers itself from
// init, so a host enables Telegram with a blank import and a rebuild. It receives
// updates by long polling, so the daemon needs no public URL, and it renders the
// live turn stream itself: a draft in a private chat, a spaced edit in a group.
package telegram
