# Third-party notices

This product includes third-party software. Their licenses and attribution
notices are reproduced below.

## Apache SpamAssassin rules

The anti-spam engine vendors the stock rule set from the Apache SpamAssassin
project, used under the Apache License, Version 2.0.

- Files: `internal/antispam/sarules/*.cf`
- License: `internal/antispam/sarules/LICENSE` (Apache License 2.0)
- Notice: `internal/antispam/sarules/NOTICE`
- Upstream: https://spamassassin.apache.org/

Only the subset of rules the evaluator supports (header, body, rawbody, uri, and
meta rules) is used; network- and plugin-backed rules are not evaluated.

This product includes software developed by the Apache Software Foundation
(http://www.apache.org/). SpamAssassin is a trademark of the Apache Software
Foundation.

## Knip configuration schema

The webmail's knip configuration points its editor schema at a local copy of the JSON schema published with knip 6.38.0, so editing it needs no network access.

- File: `internal/webmail2/knip.schema.json`
- Upstream: https://github.com/webpro-nl/knip

```
ISC License (ISC)

Copyright 2022-2026 Lars Kappert

Permission to use, copy, modify, and/or distribute this software for any purpose
with or without fee is hereby granted, provided that the above copyright notice
and this permission notice appear in all copies.

THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES WITH
REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF MERCHANTABILITY AND
FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR ANY SPECIAL, DIRECT,
INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES WHATSOEVER RESULTING FROM LOSS
OF USE, DATA OR PROFITS, WHETHER IN AN ACTION OF CONTRACT, NEGLIGENCE OR OTHER
TORTIOUS ACTION, ARISING OUT OF OR IN CONNECTION WITH THE USE OR PERFORMANCE OF
THIS SOFTWARE.
```
