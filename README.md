# Monogatari 物語

A multiplayer Japanese sentence-ordering game, played like a visual novel. Players of different JLPT levels join one room, and an AI writes a short story for them. Each player is one character. On your turn you put the words of your line in order, read it aloud, and answer a quiz about it. Everyone else watches live and helps by voice. It is made to be played on a voice call, for example on Discord, and a game takes about 10 to 15 minutes.

Go server, [Datastar](https://data-star.dev) UI, [templ](https://templ.guide) views, stories from OpenRouter, Gemini, or OpenCode Go.

## Screenshots

Offline mock story, solo game, on a phone-sized screen.

<table>
<tr><td align="center" width="25%"><img src="docs/screenshots/01-join.png" alt="Join and pick a level" width="180"><br><sub>Join and pick a level</sub></td><td align="center" width="25%"><img src="docs/screenshots/11-lobby-multiplayer.png" alt="Lobby with two players" width="180"><br><sub>Lobby with two players</sub></td><td align="center" width="25%"><img src="docs/screenshots/03-generating.png" alt="The story is written" width="180"><br><sub>The story is written</sub></td><td align="center" width="25%"><img src="docs/screenshots/04-briefing.png" alt="Briefing, lines arriving" width="180"><br><sub>Briefing, lines arriving</sub></td></tr>
<tr><td align="center" width="25%"><img src="docs/screenshots/05-scene.png" alt="Your turn" width="180"><br><sub>Your turn</sub></td><td align="center" width="25%"><img src="docs/screenshots/06-reorder.png" alt="Order the words" width="180"><br><sub>Order the words</sub></td><td align="center" width="25%"><img src="docs/screenshots/07-reading.png" alt="Read it aloud" width="180"><br><sub>Read it aloud</sub></td><td align="center" width="25%"><img src="docs/screenshots/08-quiz.png" alt="Quiz" width="180"><br><sub>Quiz</sub></td></tr>
<tr><td align="center" width="25%"><img src="docs/screenshots/09-result.png" alt="Answer and explanation" width="180"><br><sub>Answer and explanation</sub></td><td align="center" width="25%"><img src="docs/screenshots/10-finished.png" alt="Wrap-up" width="180"><br><sub>Wrap-up</sub></td></tr>
</table>

## How a game works

1. **Join.** Enter a name and pick a level on a 15-step scale, from Low N5 to High N1.
2. **Ready vote.** The game starts when 2 or more players are connected and all pressed Ready (or one player, in solo mode).
3. **Generation.** The server writes the story, then the full dialogue, with every line at its speaker's level.
4. **Briefing.** Each player sees a goal, one thing to teach the others, key words and a short reading.
5. **Turns.** Strict rotation. Order the word blocks, check, read the sentence aloud, answer a quiz. Wrong answers and wrong orders can be retried or shown, never punished.
6. **Wrap-up.** Full story, stars, missed lines and every player's word list.

### Navigation

The **☰** button opens a menu on every screen: how to play, show or hide furigana, leave, and play again. Leaving during a game keeps your seat's lines: they are played for you and you keep watching. The host (first connected player, shown with a crown) can end a running game for everyone. The wrap-up has a fixed **Play again** button. English translations are hidden by default and shown with **Show translation**.

### Solo mode

Alone in the lobby, press **Play solo** and then **Ready**. Instead of a conversation you get a short narrated text about one character, written at your level. You read it sentence by sentence and play the same turn each time: order the words, read aloud, answer the quiz. Everything else is unchanged. Solo turns off if a second player joins, and needs no second player to start.

The in-app **How to play** button explains the same in more detail.

## Requirements

- Go 1.26 or newer
- [templ](https://templ.guide/quick-start/installation) (`go install github.com/a-h/templ/cmd/templ@latest`), only to edit `views.templ`
- An [OpenRouter](https://openrouter.ai), [Google AI Studio](https://aistudio.google.com), or [OpenCode Go](https://opencode.ai/docs/console/go/) API key for real stories. Without one the app plays a built-in mock story, which is enough to try the game and work on the UI.

## Setup

```sh
git clone https://ampcode.com/@elianiva/monogatari
cd monogatari
cp .env.example .env        # add OPENROUTER_API_KEY, GEMINI_API_KEY or OPENCODE_API_KEY
set -a; . ./.env; set +a
make run                    # http://localhost:8080
```

Open the page in two browsers (or two tabs: every tab is its own player) and join with different names.

| Command | What it does |
|---|---|
| `make run` | Generate templates, build, and start the server |
| `make build` | Build `bin/monogatari` |
| `make test` | Generate templates and run the tests with the race detector |
| `make tools` | Install the pinned dev tools (templ, golangci-lint, actionlint, shellcheck) |
| `make fmt` | Format with gofumpt |
| `make lint` | Check formatting and run golangci-lint |
| `make check` | Everything CI runs |
| `make generate` | Run `templ generate` |

The generated `*_templ.go` files are committed, so building does not need templ.

## Configuration

All settings are environment variables.

| Variable | Default | Meaning |
|---|---|---|
| `LLM_PROVIDER` | auto | `openrouter`, `gemini`, `opencode-go` or `custom`. Empty uses whichever key is set |
| `OPENROUTER_API_KEY` | none | Turns on real generation through OpenRouter |
| `GEMINI_API_KEY` | none | Turns on real generation through Gemini. `GOOGLE_API_KEY` works too. The free tier allows 5 requests a minute per model, so the default list spans two models; set `JUDGE=off` there to halve the calls |
| `OPENCODE_API_KEY` | none | Turns on real generation through OpenCode Go, on `longcat-2.5-free` by default |
| `BASE_URL` | `https://ai.naufal.work` | Base URL for `custom`, an OpenAI-compatible proxy. `/v1` is added when missing |
| `AUTH_TOKEN` | none | Token for `custom`. Turns on real generation through that proxy, on `gpt-6-luna` by default |
| `MODELS` | per provider | Comma-separated model ids, `deepseek/deepseek-v4.1-flash-1017` on OpenRouter, `gemini-3.6-flash,gemini-3.5-flash` on Gemini, `longcat-2.5-free` on OpenCode Go, and `gpt-6-luna` on `custom`. Retries rotate through the list |
| `PROVIDER` | `stepfun` | OpenRouter upstream provider to pin. Empty turns pinning off. If the provider is down, requests fail. Ignored by Gemini |
| `REASONING` | `low` | Reasoning effort sent to the model on OpenRouter. `off` omits it. Ignored by Gemini |
| `JUDGE` | on | `off` skips the review call for each line |
| `ROOM_PASSPHRASE` | none | If set, the whole app shows only a passphrase screen until it is entered. Wrong tries are limited to 5 per 10 minutes per address |
| `MAX_GAMES_PER_HOUR` | `10` | Cap on stories started per hour |
| `PORT` | `8080` | Listen port |

Set `ROOM_PASSPHRASE` before putting the app on a public address: without it, anyone with the link can watch games and start stories. Every story costs model calls on your key.

## How it works

- **One room, in memory.** There is a single global room. State is lost on restart, and the app must run as a single instance.
- **Server-driven UI.** Each browser opens one SSE stream (`/stream`). Whenever the room changes, the server re-renders that player's page and Datastar morphs it into the DOM. Buttons send small `POST` requests. The browser holds no game logic.
- **Identity.** A random token in `sessionStorage` identifies a player. It is never put in a page: pages only carry a separate public id. A refresh keeps your seat. A new tab is a new player.
- **Generation** (`llm.go`, `prompts/`):
  1. A plan call writes the scenario, characters, per-player briefings and one beat per line. With one player it plans a narrated text instead of a conversation.
  2. A script call writes all lines in one pass so they read as one conversation.
  3. Each line is checked by code (furigana, chunk count for the level, one sentence, valid orders, quiz shape), then by a second review call. Lines that fail are rewritten one at a time with the earlier dialogue as context.
- **Dropouts.** If the active player disconnects, the server plays their turn with the right answers after 8 seconds. Auto-played turns earn no stars. The host (first connected player) can also skip a turn or remove someone in the lobby.

### Layout

| File | Contents |
|---|---|
| `main.go` | HTTP routes, SSE stream |
| `room.go` | Room state machine: lobby, turns, auto-play, scoring |
| `llm.go` | Provider adapters, generation, validation retries, review call |
| `model.go` | Data types, levels, furigana parsing, line checks |
| `views_*.templ`, `views.go` | Pages, one file per screen: shell, lobby, briefing, play, wrap-up |
| `prompts/` | Prompt templates for plan, script, single line and review |
| `mock.go` | Offline story |
| `static/` | CSS, Datastar, fonts |
| `scripts/install-tools.sh` | Installs the pinned dev tools; CI and the orb setup both run it |

## Testing

```sh
make test    # tests only
make check   # what CI runs: generated files, go.mod, format, lint, tests, scripts
```

CI (`.github/workflows/ci.yml`) runs the same checks as parallel jobs. Run `make tools` once to install the pinned versions.

The tests cover the turn flow, auto-play and scoring, ready-vote resets, host powers, the passphrase and game cap, page privacy, and the line checks. Prompt quality is not covered by tests: play a game and read the log (the **Show log** link on the loading screen).

## Deployment notes

- Run one instance and keep the process running between games.
- Behind a reverse proxy, turn off response buffering for `/stream`.
- Serve over HTTPS if the room is public.

## License

Copyright (C) 2026 elianiva

This program is free software: you can redistribute it and/or modify it under the terms of the GNU General Public License as published by the Free Software Foundation, either version 3 of the License, or (at your option) any later version. It is distributed without any warranty. See [LICENSE](LICENSE).

Third-party components and their licenses are listed in [THIRD_PARTY.md](THIRD_PARTY.md).
