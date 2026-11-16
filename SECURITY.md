# Security policy

Netsession Relay runs with raw-socket privileges inside LAN-party networks, so we take reports seriously.

## Reporting

Please report vulnerabilities privately via GitHub's **"Report a vulnerability"** (Security tab of this repository)
or by e-mail to **it@netsession.net**. Do not open a public issue. We answer within a week and will credit you
in the release notes unless you prefer otherwise.

## Scope and design notes

- The engine needs `CAP_NET_RAW`/`CAP_NET_ADMIN`; everything else (web UI, database) runs unprivileged.
- The web UI is meant for the management network only. It uses server-side sessions, CSRF tokens, Argon2id
  password hashes, account lockout and a strict Content-Security-Policy.
- DHCP is never relayed; frames from other relays are dropped; rate limits and a storm brake protect the network.
- Supported versions: the latest release.
