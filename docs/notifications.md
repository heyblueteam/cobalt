# Deploy notifications

Cobalt can send one Telegram message for each terminal deployment, per
project. The message says whether the deploy succeeded, which project and
deployment it was, the commit, the merged pull request when one exists, and
how long it took.

Notifications are best effort. A missing token, a Telegram outage, or a bad
template is logged and dropped. It never changes a deploy result.

## Daemon setup

The bot token is a daemon secret. It is not part of `cobalt.json`.

```bash
cobalt server \
  --telegram-token "$TELEGRAM_BOT_TOKEN" \
  --telegram-chat "-1001234567890"
```

`--telegram-chat` is the default chat. Projects can override it. With no
token, notifications stay off.

With the compose file:

```bash
export COBALT_TELEGRAM_TOKEN=...
export COBALT_TELEGRAM_CHAT=-1001234567890
docker compose up -d
```

Create a bot with [@BotFather](https://t.me/BotFather), then get the chat ID
from [@userinfobot](https://t.me/userinfobot) or from the chat's URL.

## Project setup

Add a `notify` block to the project's `cobalt.json`:

```json
{
  "version": "1.0",
  "notify": {
    "telegram": {
      "enabled": true,
      "chat": "-1001234567890",
      "on": ["success", "failure"],
      "templateFile": ".cobalt/telegram.tmpl"
    }
  }
}
```

| Field | Meaning |
|---|---|
| `enabled` | Default `true` when the block exists. `false` turns it off without deleting the block. |
| `chat` | Overrides `--telegram-chat` for this project. |
| `on` | Events to send. Default `["success", "failure"]`. Also allowed: `canceled`, `skipped`. |
| `templateFile` | Path to a message template, relative to the directory that holds `cobalt.json`. Defaults to the built-in template. |

No `notify` block means no messages for that project.

Upgrade the daemon before you add `notify` to a repo. Older daemons reject
unknown `cobalt.json` fields.

## Pull requests

Cobalt asks GitHub which pull requests contain the deployed commit and picks
the first merged one. The GitHub App needs the **Pull requests: read**
permission. Without it, the message still sends with the commit link and no
PR number.

A direct push to the branch has no pull request, so the PR line is omitted.

## Templates

A template is plain text with Go
[`text/template`](https://pkg.go.dev/text/template) syntax, rendered as
Telegram HTML. Data values are HTML-escaped before rendering; the template
controls layout only.

Built-in default:

```gotemplate
{{if eq .Status "success"}}✅{{else if eq .Status "failure"}}❌{{else}}ℹ️{{end}} Deploy {{.Status}} · {{.Project}} (#{{.Number}})
{{.Repo}} · {{.Branch}} @ {{.ShortSHA}}
{{if .PRNumber}}<a href="{{.PRURL}}">PR #{{.PRNumber}}</a> · {{end}}<a href="{{.CommitURL}}">{{.ShortSHA}}</a>
{{if .Error}}{{.Error}}
{{end}}{{.DurationText}}{{if .Host}} · {{.Host}}{{end}}
```

Example with a time zone (GMT+1) and a shorter layout:

```gotemplate
{{if eq .Status "success"}}✅{{else}}❌{{end}} {{.Project}} #{{.Number}} · {{.DurationText}}
{{if .PRNumber}}<a href="{{.PRURL}}">PR #{{.PRNumber}}</a>{{else}}<a href="{{.CommitURL}}">{{.ShortSHA}}</a>{{end}}
{{if .Error}}{{.Error}}{{end}}
{{(.FinishedAt | tz "Europe/Rome").Format "2006-01-02 15:04"}}
```

`tz` moves a time into a named zone. An unknown zone leaves the time in UTC.

### Template fields

| Field | Type | Meaning |
|---|---|---|
| `.Status` | string | `success`, `failure`, `canceled`, `skipped` |
| `.Project` | string | Cobalt project name |
| `.Number` | int | Deployment number |
| `.Repo` | string | `owner/repo` |
| `.Branch` | string | Project branch |
| `.CommitSHA` | string | Full deployed commit SHA |
| `.ShortSHA` | string | First 7 characters of the SHA |
| `.CommitURL` | string | GitHub commit link |
| `.PRNumber` | int | Merged PR number, `0` when none |
| `.PRURL` | string | Merged PR link |
| `.Duration` | duration | Wall clock deploy duration |
| `.DurationText` | string | Duration rounded to seconds |
| `.StartedAt` | time | Deploy start |
| `.FinishedAt` | time | Deploy finish |
| `.Host` | string | Daemon public host |
| `.Domains` | []string | Project domains |
| `.Error` | string | Deploy error, empty on success |

A missing or invalid template file skips the message and logs the error.
Messages are truncated below Telegram's 4096-byte limit.
