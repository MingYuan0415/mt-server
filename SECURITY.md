# Security Policy

## Reporting

Report vulnerabilities privately through GitHub Security Advisories for this repository. Do not open a public issue containing exploit details, credentials, deployment identifiers, IP addresses, or location data.

## Supported Versions

Only the latest released minor version receives security fixes. NAS deployments should pin an immutable image digest and update after validation.

## Location Data

The GeoLite2 City database is licensed by MaxMind. Deployments must obtain their own MaxMind account credentials, run the official `geoipupdate` outside the application image, and keep the database and credentials in private volumes. The application never ships with the database and never logs client IPs, inferred coordinates, or cache keys.

## Secret Handling

The repository must never contain device tokens, QWeather private keys, persistent state, real deployment hostnames, QWeather account identifiers, or real device locations. If any secret reaches Git history, revoke and replace it before rewriting or deleting the exposed file.

The server intentionally does not log client IPs, geolocation coordinates, Authorization headers, JWTs, or secret values.

`state.json` contains the QWeather private key in plaintext with mode `0600`. A v3-to-v4 migration also retains `state.v3.backup.json` with the same permissions so a v0.2 rollback remains possible. Encryption with a key stored beside these files would not improve the host-compromise boundary, so deployments must protect and encrypt backups of the complete state volume. Device tokens are stored only as SHA-256 verifiers and cannot be recovered.

The LAN Compose template permits management credentials over HTTP and is only suitable for a trusted, firewalled network. Any public or untrusted-network deployment must use HTTPS and set `MT_ADMIN_ALLOW_INSECURE_HTTP=false`.

Authenticated weather requests either include a declared coarse location or rely on IP inference. The server accepts only the fixed location-header contract (all required headers together or none), validates and rounds coordinates, never returns or logs them, and limits rapid location changes per DeviceID. IP inference reads a client-IP header only from direct peers in the configured trusted networks and queries a local GeoLite2 City database; it never calls an online geolocation service, and the client IP is never logged or returned. When Cloudflare visitor-location headers are enabled, the coordinate pair is likewise read only from direct peers in the configured trusted networks; a malformed pair fails closed without downgrading to another source. Both the request location and the inferred location exist only in memory for the request, the per-device location-change limiter, and cache keys. Responses carry a `location_key` derived deterministically from the normalized two-decimal coordinates; it does not contain coordinates or the IP directly and is not a cryptographic anonymization (the coordinate space is enumerable). Possession of a device token still permits choosing arbitrary coordinates and consuming weather quota, so tokens must be independently named, protected, rotated, and revoked when lost.

When TLS terminates at a reverse proxy, set `MT_ADMIN_BEHIND_HTTPS_PROXY=true` and keep `MT_ADMIN_ALLOW_INSECURE_HTTP=false`. Setup records the current browser-facing HTTPS Origin in private state, and authenticated administrators can maintain up to 16 origins without restarting the service. The allowlist is intentionally independent of the internal source `Host`. The server ignores `Forwarded`, `X-Forwarded-Host`, and `X-Forwarded-Proto`; the source port must be reachable only from the trusted proxy. Client-IP inference likewise requires `MT_TRUSTED_CLIENT_IP_NETS` to be restricted to the direct proxy network; the header is otherwise ignored and `MT_TRUSTED_CLIENT_IP_HEADER` without `MT_TRUSTED_CLIENT_IP_NETS` is rejected at startup.

The current management Origin cannot be removed through the web interface. Add and verify a replacement first, sign in through it, and then remove the old Origin. Removing an Origin clears every administrator session. Offline recovery commands require the server to be stopped and acquire the same exclusive state-directory lock as the server process.

Authenticated runtime diagnostics deliberately expose only process-local provider state and counters grouped by weather data kind. They do not contain raw errors, device identities, token metadata, locations, cache keys, coordinates, or client addresses.

An uninitialized instance accepts the first same-origin setup submission without a separate setup credential. Keep it behind a firewall or upstream access policy until initialization succeeds. CSRF protection prevents cross-origin browser submission but is not an authorization boundary for another client that can directly reach the service.
