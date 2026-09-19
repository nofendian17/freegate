package translate

import "regexp"

// MinOpencodeClientMajor/MinOpencodeClientMinor is the Zen gateway floor:
// User-Agents below opencode/1.17.x are rejected (426/403). freegate mints
// its own User-Agent from the cached client version (see upstream
// opencode_identity.go), which is always at or above this floor.
const (
	MinOpencodeClientMajor = 1
	MinOpencodeClientMinor = 17
)

// OpenCodeSessionRE is the canonical descending session form freegate
// mints per request: ses_ + 12 lowercase hex timestamp digits + 14 Base62
// chars. The gateway validates this shape for free-tier requests.
var OpenCodeSessionRE = regexp.MustCompile(`^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`)
