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

> **Not released yet.** The `curl | sh` installer with checksum verification is
> still missing, so build from source for now.

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
delimited block with each one's local path:

```
<!-- delivery:resources -->
- `scripts/extract.py` → `/home/you/.config/delivery/cache/.../scripts/extract.py`
<!-- /delivery:resources -->
```

## State

| Path | Contents |
|---|---|
| `$XDG_CONFIG_HOME/delivery/trust.json` | Your trust decisions, `0600` required |
| `$XDG_CONFIG_HOME/delivery/cache/content/` | Approved content for each skill |
| `~/.claude/skills/delivery/SKILL.md` | The anchor skill |

`DELIVERY_HOME` overrides the state root, `DELIVERY_CLAUDE_HOME` the Claude Code
root, and `DELIVERY_REGISTRY_BASE` the registry.

## Not there yet

Install script, public release, Windows, private skills and authentication,
agents and MCP servers and hooks, and tools other than Claude Code (the adapter
layer exists and `--tool` already takes the parameter).

## Licence

MIT. See [LICENSE](LICENSE).
