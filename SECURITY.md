# Security policy

Netsession Relay runs with raw-socket privileges inside LAN-party networks, so we take reports seriously.

## Reporting

Please report vulnerabilities privately via GitHub's **"Report a vulnerability"** (Security tab of this repository)
or by e-mail to **it@netsession.net**. Do not open a public issue. We answer within a week and will credit you
in the release notes unless you prefer otherwise.

## Scope and design notes

- The engine needs `CAP_NET_RAW`/`CAP_NET_ADMIN`; everything else (web UI, database) runs unprivileged.
- The web UI is meant for the management network only. It uses server-side sessions, CSRF tokens on every write
  (logging out included), Argon2id password hashes and a strict Content-Security-Policy. Failed logins are slowed
  down per client address (an IPv6 client: its /64) and account name: after three attempts every further one has
  to wait for a pause that doubles up to five minutes. An attempt is counted when it arrives, before the password
  is checked, so attempts sent in parallel do not get around the pause; a second, coarser budget limits all
  logins from one address. Accounts are never locked: a lock would let anybody keep the owner out.
- DHCP is never relayed; frames from other relays are dropped; rate limits and a storm brake protect the network.
- Supported versions: the latest release.
