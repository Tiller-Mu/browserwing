package cli

import (
	"encoding/json"
	"fmt"
	"os"
)

const (
	ExitOK             = 0
	ExitGeneralError   = 1
	ExitConnectError   = 2
	ExitScriptNotFound = 3
	ExitScriptFailed   = 4
	ExitBadArgs        = 64
)

type cliError struct {
	Code    int    `json:"code"`
	Error   string `json:"error"`
	Hint    string `json:"hint,omitempty"`
	Details string `json:"details,omitempty"`
}

func exitWithError(code int, msg string, hint string) {
	if isJSONStderr() {
		e := cliError{Code: code, Error: msg, Hint: hint}
		out, _ := json.Marshal(e)
		fmt.Fprintln(os.Stderr, string(out))
	} else {
		fmt.Fprintf(os.Stderr, "Error: %s\n", msg)
		if hint != "" {
			fmt.Fprintf(os.Stderr, "Hint: %s\n", hint)
		}
	}
	os.Exit(code)
}

func isJSONStderr() bool {
	return os.Getenv("BROWSERWING_JSON_ERRORS") == "1"
}
