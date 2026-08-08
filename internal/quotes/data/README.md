# Quote corpus

Every `*.json` file in this directory is embedded into the binary and flattened
into one pool at startup. To add quotes, append to an existing file. To add a
category, drop in a new file — `general.json`, `stoic.json`, `training.json` all
load the same way. No Go code changes either way.

Each file is a JSON array of objects:

```json
{
  "id": "camus-invincible-summer",
  "text": "In the depth of winter, I finally learned that within me there lay an invincible summer.",
  "author": "Albert Camus",
  "source": "Return to Tipasa",
  "tags": ["resilience"]
}
```

| Field    | Required | Notes |
| -------- | -------- | ----- |
| `id`     | yes      | Stable slug, unique across **all** files. Usually `author-fragment`. Changing an id changes which day a user sees the quote, so treat it as permanent. |
| `text`   | yes      | The quote itself, no surrounding quotation marks — the UI supplies those. Must be unique across all files. |
| `author` | yes      | Attribution as you want it displayed. |
| `source` | no       | The work it comes from. **Only fill this in when you have actually verified it.** An empty `source` is the honest signal that the attribution is the popular one rather than a checked one. |
| `tags`   | no       | Free-form labels. Nothing reads them yet; they exist so selection can later draw from a themed pool without a data migration. |

## Rules the tests enforce

`quotes_test.go` and `data_test.go` fail the build on any of these:

- a file that is not valid JSON, or has a key not in the table above (guards
  against typos like `"auther"` silently dropping a field)
- a missing or empty `id`, `text`, or `author`
- a duplicate `id` or a duplicate `text`, across all files
- an empty corpus

## On attribution

Several quotes in `general.json` carry the attribution they circulate under
rather than a verified one — the Buddha and Twain lines are not traceable to
either, the Lao Tzu line is a modern paraphrase, and the Sinatra one is likely
apocryphal. They ship as-is deliberately. If you verify one, add its `source`;
if you find one is wrong, fix the `author` and keep the `id`.
