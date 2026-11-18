# opencc

opencc lets any tool that speaks the OpenAI API talk to a running Claude Code session.

You point your tool at opencc instead of at OpenAI. Your question goes to one of your Claude Code sessions, the session answers, and the answer comes back in the format your tool expects. Editors, chat UIs, scripts, SDKs: if it can set a base URL, it can use a Claude Code session as its model.

## Picking the session

There is no real API key. The key is the name of the session you want to talk to.

If you have a session named `reviewer`, then `reviewer` is your API key. Start a session with a name like this:

```console
$ claude -n reviewer
```

or rename a running one with `/rename reviewer`. Ask your tool for the list of models and you get the names of your live sessions, so most tools will show a picker with the right things in it.

## Running it

You need Go, or Nix.

```console
$ go build -o opencc ./cmd/opencc
$ ./opencc
opencc: http://127.0.0.1:8080  socket /run/user/1000/cc-socks/ircc-opencc.sock
```

With Nix, `nix build` gives you the same binary in `result/bin/opencc`.

opencc only listens on your own machine (loopback). It refuses to start on any other address, on purpose: anything that can reach it can pick any of your sessions, so keep it local.

## Using it

Any OpenAI client works. Set the base URL and use the session name as the key:

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

The `model` field is echoed back and never used for routing. The key decides who answers.

## What you can do

- Chat: `/v1/chat/completions`, with or without streaming.
- Tool calling, so agents and function-calling clients can work. The session decides when a tool is needed. Sometimes it answers directly instead, so don't assume every request ends in a tool call.
- The newer `/v1/responses` API, including tools, plus fetching and deleting a stored response.
- Legacy `/v1/completions`.
- `GET /v1/models` and `GET /v1/models/{id}`, listing your live sessions.
- JSON answers: `response_format` with `json_object` or a JSON schema is passed to the session as an instruction.
- A base URL with or without `/v1` both work, and browsers can call it too (CORS is on).

Settings like `temperature`, `top_p`, `max_tokens` and `seed` are accepted so your client doesn't break, but they don't change anything. Ask for ignored settings and the reply tells you which ones in an `X-Opencc-Ignored` header.

## What you can't do

A Claude Code session produces text, so the parts of the OpenAI API that need more than that answer with a clear `501` error: embeddings, images, audio, moderations, files, batches, fine-tuning, assistants and the like. Same for things like `n` above 1 or `logprobs`, which answer `400`.

Streaming sends the whole reply as one piece, because the session answers all at once. Replies carry no token counts, since the session doesn't report any and I'd rather not make numbers up.

A session handles one request at a time. A second request to the same session waits for the first.

## Options

```console
$ ./opencc --help
```

The ones you'll use:

- `--listen` address to listen on, loopback only (default `127.0.0.1:8080`)
- `--timeout` how long to wait for a session to answer (default `120s`)
- `--sessions-dir` where Claude Code keeps its session list (default `~/.claude/sessions`)
- `--socks-dir` where the session sockets live (default `$XDG_RUNTIME_DIR/cc-socks`)
- `--from-mode` `prompting` or `bypass`. If your session runs in bypass mode and never seems to get your requests, try `bypass`. I haven't checked every permission mode.

## When something goes wrong

Errors come back in the OpenAI error format, so your client shows something useful.

- `401`: no key. Send the session name as the key.
- `404`: no live session has that name. Check `/v1/models`.
- `409`: two live sessions share the name. Rename one.
- `504`: the session didn't answer in time. It may be busy, or you can raise `--timeout`.
