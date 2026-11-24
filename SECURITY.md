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
- **First start:** until the first account exists, the web UI shows the setup page to whoever opens it first.
  Run the setup before the management address is reachable by others.
- **The host is shielded by the engine itself:** no IP forwarding on the relay interfaces, and an nftables table
  that lets only ping and IGMP in from the player networks, so neither SSH nor the web UI is reachable through the
  relay addresses. The engine never touches the interface with the default route unless it is listed in
  `NSRELAY_ALLOWED_IFACES`, and it keeps hard bounds on every rate limit whatever the configuration says.
- **Relay signals are signed:** the beacon that keeps two relays from forwarding the same ports carries an
  HMAC-SHA256 with a key of the installation; a forged or foreign beacon is ignored and raises an alarm.
- DHCP is never relayed; frames from other relays are dropped; rate limits and a storm brake protect the network.
- Request bodies and uploads are size-limited; an uploaded logo is decoded and re-encoded, never served as sent.
- The image is distroless and read-only; the web part runs as an unprivileged user, the engine with
  `NET_RAW`, `NET_ADMIN` and `CHOWN` only.
- CI runs govulncheck, staticcheck and the tests with the race detector on every change.
- Supported versions: the latest release.
