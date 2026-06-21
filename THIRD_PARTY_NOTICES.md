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
