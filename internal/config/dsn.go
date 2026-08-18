package config

import (
	"net/url"
	"regexp"
	"strings"
)

// RedactDSN masks the password in a PostgreSQL DSN for display in `storage
// status` and error messages. It handles both forms pgx accepts: URL DSNs
// (postgres://user:pass@host/db) and keyword/value DSNs (host=... password=...).
// A DSN it cannot parse is returned with any password-like token blanked
// conservatively, never echoed verbatim.
func RedactDSN(dsn string) string {
	if dsn == "" {
		return ""
	}
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		if u, err := url.Parse(dsn); err == nil {
			if _, hasPw := u.User.Password(); !hasPw {
				return dsn // no password to hide
			}
			// Surgery on the raw string keeps the DSN readable — url.String()
			// would percent-encode the mask. The password sits between the first
			// ':' of the userinfo and the '@' that ends it (a literal '@' or ':'
			// inside a valid URL password is percent-encoded, so these delimiters
			// are unambiguous).
			schemeEnd := strings.Index(dsn, "//") + 2
			at := strings.Index(dsn[schemeEnd:], "@")
			userinfo := dsn[schemeEnd : schemeEnd+at]
			if colon := strings.Index(userinfo, ":"); colon >= 0 {
				return dsn[:schemeEnd+colon+1] + "****" + dsn[schemeEnd+at:]
			}
			return dsn
		}
		// Unparseable URL: fall through to the keyword blanker so a password is
		// never returned verbatim.
	}
	return keywordPasswordRe.ReplaceAllString(dsn, "${1}****")
}

// keywordPasswordRe matches a `password=<value>` token in a keyword/value DSN,
// capturing everything up to and including the `=` so the value is replaced.
var keywordPasswordRe = regexp.MustCompile(`(?i)(password\s*=\s*)\S+`)
