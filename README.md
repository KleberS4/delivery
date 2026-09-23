# delivery

Single-binary Go CLI that loads Agent Skills on demand, without paying for them
in context.

A skill is a prompt. Loading dozens up front eats the context window before the
work starts. `delivery` keeps one small permanent reference in context and pulls
the full prompt only at the moment it is needed.

```
        ▓▓██▓▓
    ▓▓▓▓▓▓██▓▓▓▓▓▓
  ▓▓▓▓▓▓▓▓██▓▓▓▓▓▓▓▓   delivery  0.1.0-dev
  ▒▒▒▒▓▓▓▓██▓▓▓▓░░░░   Skills arrive when the task needs them, not before.
  ▒▒▒▒▒▒▒▒██░░░░░░░░   1 skill in the index, using 1.9 KB of the anchor's 16 KB budget
     ▒▒▒▒▒██░░░░░
         ▒██░
```

> **No release cut yet.** The installer below is in place, but it needs a
> published release to fetch from. Until the first tag, build from source.

## Install

```console
$ curl -fsSL https://raw.githubusercontent.com/KleberS4/delivery/main/install.sh | sh
```

Linux and macOS, amd64 and arm64. The binary is checked against the release's
`SHA256SUMS` before it is installed; a mismatch aborts and installs nothing.
`DELIVERY_VERSION` pins a tag, `DELIVERY_INSTALL_DIR` changes the destination
(default `~/.local/bin`).

## Build

```console
$ make build      # ./delivery
$ make check      # fmt, vet, staticcheck, tests, race detector
$ make release    # static binaries for 4 platforms, with SHA256SUMS
```

Go 1.25.13 or newer — earlier patch releases carry known standard-library
vulnerabilities in the TLS, x509, url and tar paths this tool actually uses.
The binary is static and has no runtime dependencies.

## Use

```console
$ delivery init                         # install the anchor skill
$ delivery search pdf                   # find a skill
$ delivery trust anthropics/skills/pdf  # review the content, approve it
$ delivery get pdf                      # the agent loads the prompt itself
```

`trust` is the security gate and the curation mechanism at once: it requires a
human at a terminal, and what it approves is what the agent can see. That is why
no interactive prompt ever has to appear mid-session, and why the index holds the
dozens of skills you chose rather than a registry's thousands.

It records the **hash of the content you saw**, not the skill's name. Every `get`
verifies that hash first. If the content changed upstream the command fails — it
does not warn and deliver, and it does not repair itself. Review and re-approve.

## Commands

| Command | Does | Terminal |
|---|---|---|
| `delivery` | Show where you are and what to run next | no |
| `delivery init` | Set up local state and install the anchor skill | no |
| `delivery search <term>` | Find skills in the registry | no |
| `delivery trust <ref>` | Show the content and record your approval | **yes** |
| `delivery trust --source <owner/repo>` | Trust a whole repository | **yes** |
| `delivery untrust <name>` | Revoke trust | no |
| `delivery get <name>` | Write the skill's prompt to stdout | no |
| `delivery install <name>` | Write the skill into the tool's directory | no |
| `delivery uninstall <name>` | Remove what delivery wrote | no |
| `delivery update [name]` | Show upstream changes and apply what you accept | not with `-y` |
| `delivery list` | Show what is trusted and installed | no |

`trust --source` reaches skills not yet published in that repository, which you
therefore have not reviewed. Trusting one skill at a time is safer and is the
default.

`--tool` picks where skills are installed: `claude` (default) writes to
`~/.claude/skills`, `codex` to `~/.agents/skills`, which is where Codex reads
personal skills — not `~/.codex`, which holds its config and credentials. Trust
is shared: approving a skill once makes it available to every tool.

## References

```
owner/repo/id          registry identifier (copied from search)
gh:owner/repo/path     skill directory in a GitHub repository
gh:owner/repo/file.md  skill file in a GitHub repository
gh:owner/repo/path@v1  at a specific tag or branch
https://.../skill.md   markdown at a URL
./my-skill             local directory or file
```

A registry identifier is not a path: `anthropics/skills/pdf` names the repository
`anthropics/skills` and the directory `pdf`, which `delivery` locates inside it
(the real path being `skills/pdf/`).

## Output contract

`delivery` is consumed by an agent inside a session, so its output is a machine
interface: **stdout carries the payload only**, every diagnostic goes to stderr,
and exit codes are stable.

| code | meaning |
|---|---|
| 0 | success; the payload on stdout is valid |
| 1 | incorrect usage |
| 2 | skill missing at the source |
| 3 | skill not trusted — a human has to approve it |
| 4 | content differs from what was approved |
| 5 | network failure with no usable cache |
| 6 | command requires an interactive terminal |
| 7 | invalid local state |

Codes 2 and 3 are distinct because the agent needs to know whether to look for
another skill or to call the person. Every error message carries the command that
resolves it.

Colour is disabled when the output is piped, when `NO_COLOR` is set, or with
`--no-color`.

When a skill ships supporting files, `get` downloads them too and appends a
delimited block naming the directory they sit in:

```
<!-- delivery:resources -->
This skill's 60 supporting files are on disk under:

	/home/you/.config/delivery/cache/content/anthropics-skills-docx-.../resources

Paths the skill refers to are relative to that directory.
<!-- /delivery:resources -->
```

The block names the directory and stops there, whatever the file count. It once
listed every path, which cost more context than the skill itself: for a skill of
60 files the listing ran to 12 KB against a 7 KB body, and 43% of it was the
same cache prefix repeated on every line — paid on every `get`, to hand over an
inventory the agent would open five entries of.

The block exists at all because `get` writes the body to stdout, where a
relative reference like `references/api.md` has no directory to resolve against.
`install` needs no such indirection: it writes the supporting files beside the
`SKILL.md`, reproducing the published layout, so relative references resolve
exactly as they do for a skill placed there by hand. Reinstalling clears files
the new version no longer ships.

## State

| Path | Contents |
|---|---|
| `$XDG_CONFIG_HOME/delivery/trust.json` | Your trust decisions, `0600` required |
| `$XDG_CONFIG_HOME/delivery/cache/content/` | Approved content for each skill |
| `~/.claude/skills/delivery/SKILL.md` | The anchor skill, for Claude Code |
| `~/.agents/skills/delivery/SKILL.md` | The anchor skill, for Codex |

`DELIVERY_HOME` overrides the state root, `DELIVERY_CLAUDE_HOME` and
`DELIVERY_CODEX_HOME` the respective tool roots, and `DELIVERY_REGISTRY_BASE`
the registry.

## Not there yet

Windows, private skills and authentication, agents and MCP servers and hooks,
and tools beyond Claude Code and Codex (the adapter layer takes a new one in
about thirty lines).

## Licence

MIT. See [LICENSE](LICENSE).
