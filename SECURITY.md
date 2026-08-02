# Security Policy

## Reporting

Report vulnerabilities privately through GitHub Security Advisories for this repository. Do not open a public issue containing exploit details, credentials, deployment identifiers, IP addresses, or location data.

## Supported Versions

Only the latest released minor version receives security fixes. NAS deployments should pin an immutable image digest and update after validation.

## Secret Handling

The repository must never contain device tokens, QWeather private keys, persistent state, real deployment hostnames, QWeather account identifiers, or real device locations. If any secret reaches Git history, revoke and replace it before rewriting or deleting the exposed file.

The server intentionally does not log client IPs, geolocation coordinates, Authorization headers, JWTs, or secret values.

`state.json` contains the QWeather private key in plaintext with mode `0600`. Encryption with a key stored beside the file would not improve the host-compromise boundary, so deployments must protect and encrypt backups of the complete state volume. Device tokens are stored only as SHA-256 verifiers and cannot be recovered.

The LAN Compose template permits management credentials over HTTP and is only suitable for a trusted, firewalled network. Any public or untrusted-network deployment must use HTTPS and set `MT_ADMIN_ALLOW_INSECURE_HTTP=false`.

Authenticated weather requests include a declared coarse location. The server accepts only the fixed location-header contract, validates and rounds coordinates, never returns or logs them, and limits rapid grid changes per DeviceID. Possession of a device token still permits choosing arbitrary coordinates and consuming weather quota, so tokens must be independently named, protected, rotated, and revoked when lost.

When TLS terminates at a reverse proxy, `MT_ADMIN_BEHIND_HTTPS_PROXY=true` unconditionally marks the browser-facing management transport as HTTPS. Set it only when the origin is reachable exclusively through the intended HTTPS proxy; the server intentionally ignores `X-Forwarded-Proto`.

An uninitialized instance accepts the first same-origin setup submission without a separate setup credential. Keep it behind a firewall or upstream access policy until initialization succeeds. CSRF protection prevents cross-origin browser submission but is not an authorization boundary for another client that can directly reach the service.
