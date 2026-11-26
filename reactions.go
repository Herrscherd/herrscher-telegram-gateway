package telegram

import "strings"

const variationSelector = "️"

func offeredReactions() []string {
	return strings.Fields("❤ 👍 👎 🔥 🥰 👏 😁 🤔 🤯 😱 🤬 😢 🎉 🤩 🤮 💩 🙏 👌 🕊 🤡 🥱 🥴 😍 🐳 ❤‍🔥 🌚 🌭 💯 🤣 ⚡ " +
		"🍌 🏆 💔 🤨 😐 🍓 🍾 💋 🖕 😈 😴 😭 🤓 👻 👨‍💻 👀 🎃 🙈 😇 😨 🤝 ✍ 🤗 🫡 🎅 🎄 ☃ 💅 🤪 🗿 🆒 💘 🙉 🦄 😘 💊 🙊 😎 👾 🤷‍♂ 🤷 🤷‍♀ 😡")
}

func nearestReactions() map[string]string {
	return map[string]string{
		"✅": "👍", "✔": "👍", "☑": "👍", "❌": "👎", "✖": "👎", "⚠": "🤔", "⏳": "👀", "⌛": "👀",
		"⏹": "🙈", "🛑": "🙈", "🚀": "🔥", "💥": "🤯", "❓": "🤔", "👋": "🤗", "🙂": "😁", "😀": "😁",
	}
}

func offeredReaction(emoji string) (string, bool) {
	bare := strings.ReplaceAll(strings.TrimSpace(emoji), variationSelector, "")
	for _, offered := range offeredReactions() {
		if offered == bare {
			return offered, true
		}
	}
	nearest, found := nearestReactions()[bare]
	return nearest, found
}
