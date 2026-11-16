# UK MTD VAT return (HMRC sandbox) — Universal Till plugin

A WASM (`GOOS=wasip1 GOARCH=wasm`) `export` plugin: a **sandbox-only Making
Tax Digital (MTD) VAT return bridge**. It computes Box 1 and Box 6 from the
till's archived day-closes and submits the return server-to-server to HMRC's
**test** API. It lives only on the manual Data → Export path, never on
checkout, so no sale ever waits on it (ADR-0003). It **cannot send a real
return to HMRC**. Read the [Status](#status) table before relying on it for
anything.

## Why this is its own plugin

Until `ut-docs#6289` this export was part of
[`ut-plugin-tax-uk`](https://github.com/universaltill/ut-plugin-tax-uk)
(v1.2.0 to v1.3.x, `ut-docs#1475`). A new UK till now installs the tax plugin
automatically at setup ([ADR-0172](https://github.com/universaltill/ut-docs/blob/main/adr/0172-a-new-till-auto-installs-its-countrys-tax-rate-plugin.md)),
and an auto-installed plugin may request no `net:` host (§2.4, §6) because the
operator never sees its permission badge. This export needs
`net:test-api.service.hmrc.gov.uk`, so it moved here (owner decision,
2026-11-12, ADR-0172 §9 Q3) and is **installed by choice only**: nothing
installs it automatically, and it declares `markets: ["GB"]` only so the
marketplace shows it for UK shops. The accountant CSV export
(`vat-figures-uk`, no HMRC connection) stayed in `ut-plugin-tax-uk`.

## Open work carried over

- **`ut-docs#5082`** — run this bridge end to end against the real HMRC
  sandbox (obligations, submit, view return, Fraud Prevention Header
  validation) with a sandbox test organisation. Until that is done every
  fixture in `src/hmrcvat/client_test.go` stays flagged
  `NEEDS SANDBOX VERIFICATION`. Part of epic `ut-docs#5053`.
- **`ut-docs#3863`** — send real `Gov-Client-*` fraud prevention headers on
  this export through the device-info host functions (ADR-0140 §4); blocked
  on `ut-docs#3862`. Follow-up from `ut-docs#3420`. See "Known gaps".

## Install

By choice, from the marketplace, on a UK till (`markets: ["GB"]`,
`min_pos_version` 0.30.11, the first till release that carries manifest
`markets`, `ut-docs#3176`). Permissions: `events:receive`, `sales:read`
(the day-closes), `storage` (the cached HMRC token) and
`net:test-api.service.hmrc.gov.uk` (HMRC's sandbox, the only host).

## How it works

Export entry `mtd-vat-return-uk` (label key `mtd_uk.entry_label`,
entity `eod_closes`). Run it from **Data → Export** with a from/to range
that is **exactly** one open VAT period. The plugin then:

1. Validates every `hmrc_*` setting below — any empty or malformed one
   refuses the export **by name**, before any network call.
2. Computes the return from the archived day-closes (Z-reports) the till
   sends — never re-queried from live sales, so the return always agrees
   with the Z-reports the merchant holds (`src/hmrcvat`):
   - **Box 1** `vatDueSales` = sum of the closes' cross-tab `tax`
     (returns are already negative cells; vouchers and tips are already
     excluded by core).
   - **Box 6** `totalValueSalesExVAT` = sum of cross-tab `net`, whole
     pounds, pence left out (truncated toward zero).
   - A close whose **cross-tab is empty** is refused by Z-number, naming its
     window and the unexplained amount, when gross − refunds − multi-purpose
     vouchers issued ≠ 0 or its net VAT ≠ 0 (an archive row from before the
     cross-tab existed; a sale and an equal return at different VAT rates
     zero the gross but not the VAT),
     rather than silently counted as zero. A voucher-only close is accepted
     and adds 0 to Box 1/6.
   - **Boxes 2, 4, 7, 8, 9** = the merchant's settings, verbatim. A till
     has no purchase ledger, so these are **never computed**.
   - **Box 3** `totalVatDue` = Box 1 + Box 2; **Box 5** `netVatDue` =
     |Box 3 − Box 4| — always derived, never typed in.
   - Pence → HMRC decimal: integer arithmetic only (`12345` → `123.45`),
     pinned by `TestAmount_MinorUnitsToHMRCDecimal`.
3. Gets an access token with HMRC's **refresh-token grant**
   (`POST {hmrc_api_base}/oauth/token`), cached in plugin storage
   (`hmrc_token`). HMRC rotates refresh tokens on every use, so the rotated
   one is stored and used next time; pasting a new `hmrc_refresh_token`
   restarts the chain from that value. If HMRC answers `401` to a cached
   access token (really expired, or revoked), the plugin refreshes once and
   retries that one read-only call — see "Known gaps" for why the cache's
   own expiry timestamp cannot be trusted.
4. `GET /organisations/vat/{vrn}/obligations?status=O` and picks the open
   obligation whose `start`/`end` equal the requested range **exactly**. No
   match → refuses and lists the open periods; it never picks the "nearest"
   period or files a partial one.
5. `POST /organisations/vat/{vrn}/returns` with `finalised: true`, and
   reports HMRC's `formBundleNumber`/`processingDate` (or HMRC's own error
   code, e.g. `DUPLICATE_SUBMISSION`) back to the Export page.

Every VAT API call carries `Accept: application/vnd.hmrc.1.0+json`, the
bearer token, and the static Fraud Prevention Headers `Gov-Vendor-Version`
(`ut-plugin-export-mtd-uk=<version>`) and `Gov-Vendor-Product-Name`
(`Universal%20Till`). See "Known gaps" for the headers it does **not** send.

### Status

| Claim | Status |
|---|---|
| Box 1/3/5/6 arithmetic and the pence → pounds conversion | **Confirmed by unit tests** (`src/hmrcvat`), incl. a refund cell, a repayment period (Box 5 absolute) and Box 6 pence truncation. Whether "sum of the day-close cross-tab" is the right Box 1/6 source for a given business (e.g. flat-rate or cash-accounting schemes, which change what Box 1/6 mean) is **not verified** — accountant question. |
| HMRC endpoint paths, `Accept` header, body field names, refresh-token grant, obligations/receipt/error response shapes | **Researched, not tested.** Taken from HMRC's public Developer Hub docs (VAT (MTD) API v1.0, Authorisation). Never run against the real HMRC sandbox. Every fixture is flagged `NEEDS SANDBOX VERIFICATION` in `src/hmrcvat/client_test.go`. |
| Boxes 6–9 as whole-pound JSON integers | **Researched, not tested** (VAT Notice 700/12 "leave out the pence"; sandbox acceptance of `404` vs `404.00` unconfirmed). |
| Call sequence, exact headers and bodies, stdout, token caching/rotation, refusals | **Confirmed against a stubbed HTTP layer**: `src/wasmrun/mtd_test.go` runs the REAL compiled `plugin.wasm` under wazero and pins token → obligations → returns, every header, both bodies, the stored token, the one-shot re-authentication on a `401` to a cached token, and the exact `exportResponse` stdout; plus refusal cases (missing setting, no closes, `eod_closes` not granted, non-matching period, HMRC 403, transport failure, foreign entry key). No real network call anywhere in CI. |
| `universal-till`'s real `http_request`/`storage_*` host functions and a real installed-plugin export through the till UI | **Not verified.** Same line `ut-plugin-tax-de` draws: the plugin half and the HTTP contract are each only checked separately. |
| Fraud Prevention Headers | **Placeholder / incomplete.** Only `Gov-Vendor-Version` and `Gov-Vendor-Product-Name` are sent. All `Gov-Client-*` device/network headers are omitted (see Known gaps). Fine for sandbox, **not lawful for production**. |
| Production submission | **Not supported, deliberately.** `hmrc_api_base` = `https://api.service.hmrc.gov.uk` is refused by the plugin, and the manifest grants only `net:test-api.service.hmrc.gov.uk`. |
| Satisfies MTD's "digital links" requirement for a given merchant | **Not verified** — a legal/accountant question about the merchant's whole bookkeeping chain, not something this plugin can answer by existing. |

### Configure (plugin settings)

None of these has a guessed default; the export refuses by name if one is
empty. Enter `0` explicitly for a box that is nil for the period.

- `hmrc_client_id`, `hmrc_client_secret` — the merchant's (or vendor's)
  HMRC Developer Hub application credentials.
- `hmrc_refresh_token` — from HMRC's browser consent, run once
  **out-of-band** (this plugin cannot run it — see below).
- `hmrc_vrn` — 9-digit VAT registration number, no `GB` prefix.
- `hmrc_api_base` — default `https://test-api.service.hmrc.gov.uk`
  (sandbox). Production is refused.
- **Per-period figures — update before every submission:**
  `hmrc_box2_vat_due_acquisitions`, `hmrc_box4_vat_reclaimed` (pounds and
  pence, e.g. `150.25`); `hmrc_box7_purchases_ex_vat`,
  `hmrc_box8_goods_supplied_ex_vat`, `hmrc_box9_acquisitions_ex_vat`
  (whole pounds). Raw plugin settings — no dedicated per-period form.

### Known gaps

- **`Gov-Client-*` Fraud Prevention Headers are not sent.** HMRC makes
  them mandatory by law for every production MTD call (`Gov-Client-
  Connection-Method`, `-Device-ID`, `-Local-IPs`, `-Timezone`, `-Screens`,
  `-User-Agent`, …). A sandboxed WASM plugin (ADR-0001) has no host
  function that exposes real device/network metadata — only `log_write`,
  `settings_get`, `http_request`, `storage_get`, `storage_set` exist — so
  these are omitted rather than fabricated, and every export logs that it
  omitted them. Fixing this needs a new device/network-metadata host
  function in `universal-till` (a host-ABI change affecting every plugin),
  tracked as its own follow-up, not this plugin.
- **No interactive OAuth consent.** HMRC's authorization-code grant is a
  browser redirect; a WASM till plugin has no browser. A self-service
  "Connect to HMRC" flow most likely belongs in the cloud/`my.` surface.
- **Boxes 2/4/7/8/9 are manual** and are not reset between periods — a
  stale value from last quarter would be re-submitted if not updated.
- **Unclosed days are not detected.** The plugin refuses when there are
  no archived closes at all, and refuses a close whose empty cross-tab leaves
  gross − refunds − multi-purpose vouchers issued (or net VAT) unexplained,
  but does not check that the closes cover every trading
  day of the period — a day that was never closed is simply missing from
  Box 1/6.
- **The plugin cannot read the real time.** `universal-till` runs plugins
  under wazero's default fake wall clock (no `WithSysWalltime` in
  `internal/plugins/wasm_runtime.go`), so `time.Now()` inside the plugin is
  ~2022-01-01 on every run. The cached access token's `expires_at` is
  therefore only advisory and never expires on its own; the real check is
  HMRC's `401`, on which the plugin re-authenticates once (pinned in
  `src/wasmrun/mtd_test.go`). A host clock function (or `WithSysWalltime`
  in core) would make the cache honest — a core follow-up, not this plugin.

### What a human still needs to do before this could ever go live

1. **Run HMRC's browser OAuth consent once, out-of-band**, for the
   merchant's VRN (scopes `read:vat write:vat`), and paste the resulting
   refresh token into `hmrc_refresh_token`. Refresh tokens expire (HMRC
   documents 18 months) and must then be re-consented.
2. **Run the bridge against the real HMRC sandbox** with a sandbox test
   organisation and replace every `NEEDS SANDBOX VERIFICATION` fixture
   with a captured real response.
3. **Close the Fraud Prevention Header gap** (the host-function follow-up
   above) and validate the headers with HMRC's Test Fraud Prevention
   Headers API.
4. **Get HMRC production approval** — HMRC reviews real Fraud Prevention
   Header evidence from a working integration before granting production
   credentials. Only then add `net:api.service.hmrc.gov.uk` and lift the
   production refusal.
5. **Get an accountant to confirm** the Box 1/6 sourcing for the
   merchant's VAT scheme, and how Boxes 2/4/7/8/9 are sourced each period.


## Configure (plugin settings)

See [Configure](#configure-plugin-settings) above; the nine `hmrc_*` keys
are this plugin's only settings.

## Build

```sh
bash scripts/build.sh   # -> bin/plugin.wasm (GOOS=wasip1 GOARCH=wasm)
go test ./...           # src/hmrcvat (host) + src/wasmrun (builds and runs the real wasm)
bash scripts/validate.sh
```

`src/main.go` is gated `//go:build wasip1`, so a plain host `go build ./...`
skips it; `src/wasmrun` builds its own wasm from the current source and
runs it under wazero (a test-only dependency). CI runs
`GOOS=wasip1 GOARCH=wasm go vet ./...`, `gofmt -l .`,
`scripts/guard-plugin-i18n.sh`, `go test ./...`, then build/validate/package.
