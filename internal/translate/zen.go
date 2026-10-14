package translate

import "regexp"

// MinOpencodeClientMajor/MinOpencodeClientMinor is the gateway floor:
// User-Agents below opencode v2 (opencode/<channel>/<version>/<client>,
// e.g. opencode/latest/2.0.26/cli) are rejected with 403 FreeTierError
// since 2026-10-14, when free inference moved from /zen/v1 to
// /inference/v1 (9router PR #4842). freegate mints its own User-Agent
// from the cached client version (see upstream opencode_identity.go),
// which is always at or above this floor.
const (
	MinOpencodeClientMajor = 2
	MinOpencodeClientMinor = 0
)

// OpenCodeSessionRE is the canonical descending session form freegate
// mints per request: ses_ + 12 lowercase hex timestamp digits + 14 Base62
// chars. The gateway validates this shape for free-tier requests.
var OpenCodeSessionRE = regexp.MustCompile(`^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`)
