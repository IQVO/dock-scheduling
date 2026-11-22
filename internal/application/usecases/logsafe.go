package usecases

import "strings"

// logReplacer strips CR and LF from a request-derived value (an error's text)
// before it is logged, so the value cannot start a forged log line (CWE-117,
// CodeQL go/log-injection).
var logReplacer = strings.NewReplacer("\r", "", "\n", "")
