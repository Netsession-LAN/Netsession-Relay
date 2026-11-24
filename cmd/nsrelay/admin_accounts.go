package main

import (
	"fmt"

	"github.com/Netsession-LAN/Netsession-Relay/internal/auth"
)

// Admin subcommands for local accounts and sessions.

func adminSetPassword(c *adminCall) int {
	role := c.fs.String("role", "admin", "role of the account: admin|operator|viewer")
	if !c.parse() {
		return 2
	}
	if c.fs.NArg() != 1 {
		return c.fail(fmt.Errorf("admin set-password [-role admin|operator|viewer] <name>"))
	}
	st, ok := c.store()
	if !ok {
		return 1
	}
	defer st.Close()
	name := c.fs.Arg(0)
	pw := auth.Password()
	hash, err := auth.Hash(pw)
	if err != nil {
		return c.fail(err)
	}
	if err := st.SetLocalUser(c.ctx, c.common.user, name, hash, *role); err != nil {
		return c.fail(err)
	}
	fmt.Fprintf(c.stdout, "account %q (%s) - password (shown only now): %s\n", name, *role, pw)
	return 0
}

func adminLogout(c *adminCall) int {
	if !c.parse() {
		return 2
	}
	if c.fs.NArg() != 1 {
		return c.usage("logout <name>")
	}
	st, ok := c.store()
	if !ok {
		return 1
	}
	defer st.Close()
	name := c.fs.Arg(0)
	n, err := st.Logout(c.ctx, c.common.user, name)
	if err != nil {
		return c.fail(fmt.Errorf("logout %s: %w", name, err))
	}
	fmt.Fprintf(c.stdout, "%s: %d session(s) ended\n", name, n)
	return 0
}
