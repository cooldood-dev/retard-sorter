# Retard Sorter

Netscape cookie sorter and domain filter with a local web UI.

## Authentication & Licensing

The desktop app gates all routes behind **license activation**. Activation is
**license-key only** — there is no account sign-in. Configuration resolution
(highest wins):

1. Environment variables
2. `supabase.env.enc` — **encrypted at rest** secret store (machine-bound, see below)
3. `supabase.env` — plaintext legacy file (local dev; deleted once encrypted)

| Variable | Purpose |
|---|---|
| `SUPABASE_URL` | Your project URL (`https://….supabase.co`) — HTTPS required |
| `SUPABASE_SERVICE_KEY` | Service role key — **owner-only**; used for the `activate_license` RPC |
| `SUPABASE_SERVICE_ROLE_KEY` | Legacy spelling, still accepted and bridged to the above |

The **service role key must never** appear in frontend files, API responses,
client-side JavaScript, or distributed builds. It is read only from the
encrypted `supabase.env.enc` on the owner's machine and is optional at runtime
for end users (activation runs with the key supplied per-build).

### Encrypting the owner's config (`supabase.env.enc`)

The plaintext `supabase.env` holds the service role key — full admin access to
the Supabase project. To keep it secret at rest, encrypt it with the machine key
(the same AES-256-GCM key that protects `session.enc`):

```text
retard-sorter.exe -encrypt-env
```

This reads `supabase.env` next to the exe, writes `supabase.env.enc` (0600,
decrypts **only on this machine**), and deletes the plaintext. Loaded
automatically at startup; env vars still win. To change values later, recreate
`supabase.env` (use `supabase.env.example`) and run `-encrypt-env` again.
A foreign or corrupt `.enc` file is silently ignored.

### Hardware binding (HWID)

Activation binds the license to the **machine's hardware**, not a network
address. The HWID is a SHA-256 over stable hardware identifiers (system/BIOS
UUID, disk volume serial, CPU name on Windows; DMI UUID + machine-id on Linux;
IOPlatformUUID on macOS). It is computed in the Go backend — never taken from
browser input or headers.

The RPC `activate_license(p_key, p_hwid)` returns exactly one of:

| RPC result | Meaning |
|---|---|
| `invalid_key` | Key does not exist |
| `revoked` | License revoked by admin |
| `hwid_mismatch` | Key is active but bound to a different machine |
| `invalid_hwid` | Internal error — HWID could not be verified |
| `activated` | Newly activated on this machine |
| `ok` | Already active on this machine |

### HWID lockout / reset (admin)

If a license needs to move to a different machine, an administrator clears the
binding in Supabase. This is an explicit admin action — there is no automatic
grace period, which prevents silent rebinding by unauthorized parties:

```sql
SELECT reset_license_hwid('LICENSE-KEY-HERE');
```

Or via PostgREST RPC (service role):

```http
POST /rest/v1/rpc/reset_license_hwid
{"p_key": "LICENSE-KEY-HERE"}
```

After reset, the user runs activation again on the new machine; the RPC binds
`bound_hwid` to the newly reported hardware.

### Startup flow

1. **Valid session** → main app (`/`)
2. **No session** → activation page (`/activate`)

### Sessions & revalidation

- There is no login. A successful activation writes a machine-bound
  `session.enc` (AES-256-GCM under the HWID-derived key) recording the
  activated key.
- Every **2 minutes** the app re-checks the license against the server
  (`activate_license` with the cached key + current HWID).
- Only the definitive verdicts — `hwid_mismatch`, `revoked`, `invalid_key` —
  clear the session and bounce the user back to activation with the reason.
  **Offline, DNS failure, or timeouts never log a user out**: a network blip
  must not invalidate a valid license.
- Password-less by design: nothing to leak, nothing to rotate.

### Security notes

- All Supabase calls use HTTPS with request timeouts.
- Error paths fail closed (deny access); raw Supabase errors are logged internally only.
- API routes (`/api/*`) return `401 JSON` when unauthenticated; HTML routes redirect to `/activate`.
