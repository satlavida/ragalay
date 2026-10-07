//go:build !windows

package cli

import "errors"

const canSetUserEnv = false

// setUserEnv is Windows-only: elsewhere the variable belongs in the shell
// profile (keyHelp says how).
func setUserEnv(name, value string) error {
	return errors.New("add the variable to your shell profile (~/.zshrc or ~/.bashrc)")
}
