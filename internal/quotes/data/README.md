# Quote corpus

Every `*.json` file in this directory is embedded into the binary and flattened
into one pool at startup. To add quotes, append to an existing file. To add a
category, drop in a new file — `general.json`, `stoic.json`, `training.json` all
load the same way. No Go code changes either way.

Each file is a JSON array of objects:

```json
{
  "id": "coelho-dream-come-true",
  "text": "It's the possibility of having a dream come true that makes life interesting.",
  "author": "Paulo Coelho",
  "author_url": "https://en.wikipedia.org/wiki/Paulo_Coelho",
  "source": "The Alchemist",
  "source_url": "https://en.wikipedia.org/wiki/The_Alchemist_(novel)",
  "tags": ["hope"]
}
```

| Field        | Required | Notes |
| ------------ | -------- | ----- |
| `id`         | yes      | Stable slug, unique across **all** files. Usually `author-fragment`. Changing an id changes which day a user sees the quote, so treat it as permanent. |
| `text`       | yes      | The quote itself, no surrounding quotation marks — the UI supplies those. Must be unique across all files. |
| `author`     | yes      | Attribution as you want it displayed. |
| `author_url` | no       | English Wikipedia article for the **person**. See [On links](#on-links). |
| `source`     | no       | The work it comes from. **Only fill this in when you have actually verified it.** An empty `source` is the honest signal that the attribution is the popular one rather than a checked one. |
| `source_url` | no       | English Wikipedia article for the work. Requires a `source`. |
| `tags`       | no       | Free-form labels. Nothing reads them yet; they exist so selection can later draw from a themed pool without a data migration. |

## Rules the tests enforce

`quotes_test.go` and `data_test.go` fail the build on any of these:

- a file that is not valid JSON, or has a key not in the table above (guards
  against typos like `"auther"` silently dropping a field)
- a missing or empty `id`, `text`, or `author`
- a duplicate `id` or a duplicate `text`, across all files
- an empty corpus
- an `author_url` or `source_url` that is not an `https://en.wikipedia.org/wiki/`
  article — catches a pasted `en.m.` mobile URL, an `http://` one, or a bare
  `/wiki/` with the title lost
- a `source_url` with no `source` — the UI renders that link *on* the source
  text, so it would have nothing to attach to
- two quotes that give the **same author** two different `author_url`s

## On links

Both URLs are optional, and English Wikipedia is the only accepted host —
standardizing on one encyclopedia is what lets the tile style every link the
same way instead of branching on the destination.

`author_url` links the **person**, not the attribution. That distinction is what
lets the apocryphal quotes below still carry a link: pointing at Mark Twain's
article says "here is who that was," not "he definitely said this." So fill it
in whenever the author has an article, regardless of whether you have verified
the quote itself. Leave it empty when there is no article to point at.

`source_url` is stricter only because `source` already is: a source is a
verified-only field, so a source link inherits that.

Prefer the canonical article title over a redirect (`/wiki/Laozi`, not
`/wiki/Lao_Tzu`), and **open the URL before committing it** — the tests check the
shape of a link, not that it resolves. A hardcoded 404 is worse than no link.

## On attribution

Several quotes in `general.json` carry the attribution they circulate under
rather than a verified one — the Buddha and Twain lines are not traceable to
either, the Lao Tzu line is a modern paraphrase, and the Sinatra one is likely
apocryphal. They ship as-is deliberately. If you verify one, add its `source`;
if you find one is wrong, fix the `author` and keep the `id`.
