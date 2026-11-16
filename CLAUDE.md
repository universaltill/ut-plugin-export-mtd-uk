# ut-plugin-export-mtd-uk — notes

A WASM (`GOOS=wasip1 GOARCH=wasm`) plugin, `canonical_type: export` (ADR-0002,
ADR-0009 naming): the sandbox-only HMRC Making Tax Digital VAT return bridge,
split out of `ut-plugin-tax-uk` by ut-docs#6289 (ADR-0172 §6). One export
entry, `mtd-vat-return-uk`, one hook, `export.requested.ask`. Manual
Data/Export path only, never checkout.

## Why it is separate (do not merge it back)

`ut-plugin-tax-uk` is auto-installed on a new UK till (ADR-0172) and so may
hold only `events:receive`, `sales:read`, `storage`. This plugin holds
`net:test-api.service.hmrc.gov.uk` and is **installed by choice only**.
Never add a `tax.rate.ask` hook or `canonical_type: tax` here, and never
copy this export back into the tax plugin.

## Rules

- Its only `net:` permission is `net:test-api.service.hmrc.gov.uk`;
  `scripts/validate.sh` refuses any other host and `hmrcvat.Settings.Validate`
  refuses HMRC production. Never fabricate: Boxes 2/4/7/8/9 come only from
  merchant settings; the `Gov-Client-*` Fraud Prevention Headers are omitted
  (no host function exposes device metadata), never invented (#3863).
- `hmrcvat.VendorVersion` must equal `manifest.json`'s version (a test pins
  it): bump both together. `time.Now()` in the guest is wazero's FAKE clock,
  so `CachedToken.ExpiresAt` is advisory; the 401-then-refresh-once path in
  `main.go` is the real expiry check, keep it and its `src/wasmrun` case.
- `min_pos_version` stays at least 0.30.11 (first till with manifest `markets`,
  ut-docs#3176); `markets: ["GB"]`.
- Every import in `main.go` needs a stub in `src/wasmrun`; any change to the
  dispatch or the wire shape gets a `src/wasmrun` case.
- Real-sandbox evidence is ut-docs#5082; production needs HMRC approval first.

## Layout

`src/main.go` (dispatch + HMRC call sequence), `src/hmrcvat/` (host-free
request builders/validators, `crosstab.go` = the shared empty-cross-tab check),
`src/wasmrun/` (test-only: real wasm under wazero, scripted HTTP stub).

## Verification

`GOOS=wasip1 GOARCH=wasm go vet ./...`, `gofmt -l .`, `go test ./...`,
`bash scripts/build.sh`, `bash scripts/validate.sh`,
`bash scripts/guard-plugin-i18n.sh` (all in CI). A PR touching shipped files
must bump `manifest.json`'s version (`scripts/check-version-bump.sh`).
Release: merge to main; `auto-tag-release.yml` tags and dispatches
`release.yml`, which publishes to the marketplace (secrets
`MARKETPLACE_BASE_URL`, `MARKETPLACE_UPLOAD_TOKEN`; var `AUTO_APPROVE`).

## Before committing

Keep README.md's status honest; behaviour changes update it in the same
session. Standards live in ut-docs (`adr/`).
