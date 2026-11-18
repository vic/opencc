# opencc

[![Sponsor](https://img.shields.io/badge/sponsor-vic-ea4aaa?logo=githubsponsors)](https://github.com/sponsors/vic)

opencc connects Claude Code sessions and OpenAI-style APIs, in both directions.

- **Use Claude Code as an OpenAI API.** Point any tool that speaks the OpenAI API at opencc, and your question goes to one of your running Claude Code sessions. The answer comes back in the format your tool expects.
- **Use any OpenAI API from Claude Code.** Run opencc as a client and it shows up as a named peer. A session sends it a message, opencc asks the remote model, and the answer comes back as a message.

Both run on your machine and talk to your sessions through the local sockets Claude Code already has.

## Getting it

You need Go, or Nix.

```console
$ go build -o opencc ./cmd/opencc
```

With Nix, `nix build` puts the binary in `result/bin/opencc`.

## Claude Code as an OpenAI API

Start opencc:

```console
$ ./opencc
opencc: http://127.0.0.1:8080  socket /run/user/1000/cc-socks/ircc-opencc.sock
```

There is no real API key. The key is the name of the session you want to talk to. Start a session with a name:

```console
$ claude -n reviewer
```

or rename a running one with `/rename reviewer`. Now `reviewer` is your API key. Any OpenAI client works with it:

```console
$ export OPENAI_BASE_URL=http://127.0.0.1:8080/v1
$ export OPENAI_API_KEY=reviewer
```

Plain curl works too:

```console
$ curl -s http://127.0.0.1:8080/v1/chat/completions \
    -H "Authorization: Bearer reviewer" \
    -d '{"model":"gpt-4o","messages":[{"role":"user","content":"What is 6 times 7?"}]}'
```

The `model` field is echoed back and never used for routing. The key decides who answers. Ask for the list of models and you get the names of your live sessions, so tools with a model picker show the right choices.

opencc only listens on your own machine (loopback) and refuses to start on any other address. Anything that can reach it can pick any of your sessions, so keep it local.

### What works

- Chat: `/v1/chat/completions`, with or without streaming.
- Tool calling, so agents and function-calling clients can work. The session decides when a tool is needed. Sometimes it answers directly instead, so don't assume every request ends in a tool call.
- The newer `/v1/responses` API, including tools, plus fetching and deleting a stored response.
- Legacy `/v1/completions`.
- `GET /v1/models` and `GET /v1/models/{id}`.
- JSON answers: `response_format` with `json_object` or a JSON schema is passed to the session as an instruction.
- A base URL with or without `/v1`, and browsers can call it too (CORS is on).

Settings like `temperature`, `top_p`, `max_tokens` and `seed` are accepted so your client doesn't break, but they don't change anything. The reply lists the ones it ignored in an `X-Opencc-Ignored` header.

### What doesn't

A Claude Code session produces text, so the parts of the OpenAI API that need more than that answer with a clear `501` error: embeddings, images, audio, moderations, files, batches, fine-tuning, assistants and the like. Things like `n` above 1 or `logprobs` answer `400`.

Streaming sends the whole reply as one piece, because the session answers all at once. Replies carry no token counts, since the session doesn't report any and I'd rather not make numbers up.

A session handles one request at a time. A second request to the same session waits for the first.

### Options

```console
$ ./opencc --help
```

- `--listen` address to listen on, loopback only (default `127.0.0.1:8080`)
- `--timeout` how long to wait for a session to answer (default `120s`)
- `--sessions-dir` where Claude Code keeps its session list (default `~/.claude/sessions`)
- `--socks-dir` where the session sockets live (default `$XDG_RUNTIME_DIR/cc-socks`)
- `--from-mode` `prompting` or `bypass`. If your session runs in bypass mode and never seems to get your requests, try `bypass`. I haven't checked every permission mode.

### When something goes wrong

Errors come back in the OpenAI error format, so your client shows something useful.

- `401`: no key. Send the session name as the key.
- `404`: no live session has that name. Check `/v1/models`.
- `409`: two live sessions share the name. Rename one.
- `504`: the session didn't answer in time. It may be busy, or you can raise `--timeout`.

## Any OpenAI API from Claude Code

Run opencc as a client and give it a name:

```console
$ export OPENAI_API_KEY=sk-...
$ ./opencc client --name gpt --model gpt-4o
opencc client: gpt -> https://api.openai.com/v1 (gpt-4o), socket /run/user/1000/cc-socks/ircc-gpt.sock
```

Now any Claude Code session on your machine can message `gpt` (with the SendMessage tool, or from ircc). The reply arrives as a message from `gpt` a moment later.

Each session that talks to `gpt` gets its own conversation, and `gpt` remembers the last 40 messages of it. Send `/reset` to start over.

It works with anything that speaks the OpenAI API. Point `--base-url` at it, up to and including `/v1`. For a local server, name an empty variable so no key is sent:

```console
$ ./opencc client --name local --model llama3 \
    --base-url http://127.0.0.1:11434/v1 --api-key-env NONE
```

You can run several clients side by side, each with its own name. Stop one with Ctrl-C and it disappears from the session list.

If the remote fails, you get its status and message as the reply (for example `opencc: 401 bad key`), so the session sees what happened and can tell you.

### Options

```console
$ ./opencc client --help
```

- `--name` the name sessions send to (required)
- `--model` the remote model id (required)
- `--base-url` the API to call (default `https://api.openai.com/v1`)
- `--api-key-env` the environment variable that holds the key (default `OPENAI_API_KEY`). The key never goes on the command line.
- `--system` a system prompt for every conversation
- `--timeout` how long to wait for the remote (default `120s`)
- `--sessions-dir`, `--socks-dir`, `--from-mode` the same as above

### Limits

Text in, text out: no images, files or tool calls to the remote. Answers arrive whole, not streamed. A reply over 512 KiB is cut, with a note saying so. Only sessions on your machine can reach a client.
