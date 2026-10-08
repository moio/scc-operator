# PLAN: Adapting `scc-operator` to SCC Organization Credentials


**Question under test:** _"Adapting scc-operator to use SCC org credentials instead of the currently
supported regcodes does not require extensive changes."_

**Verdict:** **True — and there is more than one viable path.** They differ mainly on three axes:

- **Blast radius** — lines changed, CRD change, upstream `connect-ng` PR.
- **Does a regcode remain inside the operator?** — some paths keep one internally, some eliminate it.
- **Is the activation subscription-counted?** — regcode-based paths produce a *counted, linked*
  activation; org-based paths produce an *unlinked, uncounted* one. **This is a tradeoff to weigh, not
  a hard requirement** — treat it as a pro/con below.

All statements were verified against **production** SCC (`scc.suse.com`) with real org credentials.
**Offline mode is unaffected by every option** (§6).

---

## 1. Two registration surfaces on SCC

**A. Regcode flow** (`/connect/subscriptions/*` + `/connect/systems/*`) — what the operator uses today.

| Call | Auth | Purpose |
|---|---|---|
| `POST /connect/subscriptions/systems` (announce) | `Token token=<regcode>` | create system, return `login`/`password` |
| `POST /connect/systems/products` | Basic (system) + `token=<regcode>` | activate → **counts against the subscription** |
| `GET /systems/activations`, `PUT /systems`, `DELETE /systems` | Basic (system) | status / keepalive / deregister |

→ Produces a **linked, counted** activation (`regcode` + `status=ACTIVE`, `systems_count` increments).
Needs a regcode.

**B. Org flow** (`/connect/organizations/*`) — RMT-style inventory API.

| Call | Auth | Purpose |
|---|---|---|
| `GET /connect/organizations/subscriptions` | Basic (org) | list subscriptions **incl. `regcode`** |
| `POST`/`PUT /connect/organizations/systems` | Basic (org) + `RMT: <uuid>` | create system(s) with `products[]` |
| `DELETE /connect/organizations/systems/:id` | Basic (org) + `RMT: <uuid>` | delete (same `RMT` UUID that created it) |

→ Uses **org credentials only, no regcode**. System is visible under Organization → Systems. But the
activation it records is **unlinked** (`regcode=null`, `status=null`) and **does not** move
`systems_count`.

The two surfaces are independent. Org credentials can either *derive* a regcode (feed surface A) or
*drive* surface B directly.

---

## 2. Load-bearing facts (empirical)

1. **Org credentials return regcodes.** `GET /connect/organizations/subscriptions` returns each Rancher
   Prime subscription **including its `regcode`** to an ordinary mirroring-credential holder — not
   privilege-gated. The record maps ~1:1 onto the existing `SubscriptionInfo` CRD status type
   (`product_classes` is `[]string` here vs `[]{name,desc}` on `/subscriptions/info` — ~6-line delta).

2. **`arch` must be the literal string `"unknown"`.** Rancher telemetry reports `arch: "unknown"`
   (fixture in `internal/repos/secretrepo/secrets_test.go`; passed through by
   `MetricsWrapper.GetProductIdentifier()`) and SCC keys the `rancher-prime` product on it.
   `x86_64`/`amd64`/`aarch64`/`""`/`null` all `422 No product found`; omitting it `422 missing arch`.
   The existing operator already sends this from telemetry; a hand-rolled org client must replicate it.

3. **Version must be a concrete catalogue release** (e.g. `2.14.4`). `SCCSafeVersion()` maps
   `major.minor` / pre-release / dev → `"other"`, and `rancher-prime/other` is a catalogue entry.
   The org products catalogue reports an **empty `arch`** for every Rancher product, so force `unknown`.

4. **Org bulk endpoints record an unlinked, uncounted activation.** `POST`/`PUT
   /connect/organizations/systems` with a valid `products[]` triplet returns 201 and creates an
   activation record carrying full product info (identifier, version, service) but `regcode=null`,
   `status=null`; `systems_count` does not move (measured 1 → 1). Invalid triplets are silently dropped.

5. **Custom system credentials are accepted at announce.** `POST /connect/subscriptions/systems`
   honours a caller-chosen `login`/`password` — the operator can mint stable credentials.

6. **Header/delete quirks.** Org write endpoints require `RMT: <uuidv4>`. Org-created systems delete via
   `DELETE /organizations/systems/:id` **only from the same `RMT` UUID**; the universal fallback is
   `DELETE /connect/systems` (system creds).

7. **Security asymmetry.** Org credentials are higher-privilege than a regcode: they can read the whole
   org catalogue, **every subscription's regcode**, and delete arbitrary org systems. Storing them in a
   cluster Secret is a materially different threat model than a single-product regcode.

---

## 3. Options

Line counts `added / removed`; **core** = production Go, **total** = core + tests + generated.
Each option notes the three axes: **regcode-in-operator**, **counted activation**, **blast radius**.

### Option 1 — Resolve org creds → regcode in the Secret controller ⭐ smallest

Accept `sccOrgUsername`/`sccOrgPassword` in the existing `scc-registration` Secret. When `regCode` is
absent but org creds are present, list subscriptions, pick the one whose `product_classes` cover the
Rancher class, and write its `regcode` into the existing `registration-code-<hash>` Secret. Nothing
downstream changes.

| File | Δ |
|---|---|
| `internal/suseconnect/organizations/{subscriptions,select}.go` (new) | +185 |
| `internal/consts/secrets.go` | +6 |
| `pkg/controllers/resources.go` | +55 / -8 |
| `pkg/controllers/controller.go` | +35 / -2 |
| **Core** | **+281 / -10** (5 files, 2 new) |
| Total (+ tests) | **~+470 / -10** |

- **Regcode in operator:** yes (internally). **Counted:** ✅ yes. **CRD/connect-ng:** none.
- **Pro** — smallest blast radius; reuses the certified lifecycle; ships without an upstream release;
  counted activations and accurate status for free.
- **Con** — operator holds a regcode (Secret + `RegCodeHash`); subscription selection is a policy you
  now own; org creds' broad privilege lands in the cluster.

### Option 2 — First-class org credential source (CRD-visible), regcode-backed

Option 1 promoted to a concept: a `scc-org-credentials-<hash>` Secret, a new optional
`RegistrationRequest.OrganizationCredentialsSecretRef`, resolution in `Register()` (re-resolves every
reconcile), org subscription data mapped into `status.subscriptionInfo`.

| Bucket | Δ |
|---|---|
| Option 1 + CRD/consts/finalizers/wrapper/`online.go` | +512 / -27 (~12 files) |
| Total (+ tests, generated) | **~+890 / -27** |

- **Regcode in operator:** yes. **Counted:** ✅ yes. **CRD:** yes. **connect-ng:** none.
- **Pro** — proper CRD modelling; handles regcode rotation/expiry; authoritative subscription info.
- **Con** — CRD change ⇒ chart/UI/docs coordination (§5); still regcode-backed; documents org creds as
  a first-class input (wider attack surface).

### Option 3 — Org bootstrap, system steady-state (no regcode in operator)

Mint `login`/`password`/`system_token` into the existing `scc-system-credentials-<hash>` Secret; create
the system via `POST /connect/organizations/systems` (single) with a `products[]` entry for the Rancher
triplet (`arch=unknown`); keepalive via `PUT /connect/systems` (system auth); deregister via
`DELETE /organizations/systems/:id`. `status.registeredProduct` comes from the recorded activation;
`status.registrationExpiresAt` from the org subscription. **No regcode is ever used.**

| Repo / bucket | Δ |
|---|---|
| **connect-ng** `pkg/organizations/*` (new: systems, subscriptions, pagination, auth) | +391 core / +420 tests |
| **scc-operator** credential minting + wrapper + `online.go` branch + CRD/secrets plumbing | +520 core / +380 tests |
| **Total** | **~+900 core / ~1,700** (2 repos, ~18 files) |

*Spike simplification:* the single `POST /connect/organizations/systems` works, so the 50-item
chunking / 413 back-off is unnecessary for one system (~-90 lines vs the original bulk design).

- **Regcode in operator:** **none** ✅. **Counted:** ❌ no (unlinked activation; `systems_count`
  unchanged). **CRD:** yes. **connect-ng:** upstream PR required.
- **Pro** — eliminates the regcode entirely; system visible under Organization → Systems; the
  `pkg/organizations` client is useful upstream on its own.
- **Con** — activation is uncounted and unlinked, so `registrationExpiresAt` becomes *inferred* from
  subscription data rather than observed; upstream PR + `go.mod` bump on the critical path; you own
  credential minting (rotation, `system_token` stability); org creds' broad privilege in the cluster.

### Option 4 — Pure org model (no regcode, no system-auth)

Option 3 with all state flowing through org endpoints — `registeredProduct`/`registrationExpiresAt`
synthesised from `GET /connect/organizations/subscriptions` + the triplet; keepalive as a bulk PUT with
fresh `last_seen_at`.

| | Δ |
|---|---|
| Delta over Option 3 | +580 / -90 |
| **Total** | **~+1,200 core / ~2,290** (2 repos, ~20 files) |

- **Regcode in operator:** none ✅. **Counted:** ❌ no. **CRD:** yes. **connect-ng:** PR required.
- **Pro** — single auth model; conceptually clean; foundation for fleet reporting (Option 5).
- **Con** — largest single-cluster surface; status accuracy regresses further (fully inferred);
  concentrates cost in the hardest-to-test code (`PrepareActivatedForKeepalive`). Prefer Option 3
  unless you need the pure model as a stepping stone.

### Option 5 — Org model + fleet reporting

The one thing regcodes cannot do: report **many** systems (downstream clusters) under one org
credential, batched (≤50/req, 413 back-off), with per-system records. Needs a new CRD or repeated
`Registration`, an inventory source, and per-system credential lifecycle.

- **Regcode in operator:** none ✅. **Counted:** ❌ no. Rough order **~+2,700 core / ~4,000 total**.
- **Pro** — org-wide fleet visibility, the genuine payoff of org credentials.
- **Con** — largest effort; only worthwhile behind an explicit fleet-reporting requirement.

---

## 4. Summary

| # | Option | Core LOC | Total | Regcode in operator? | Counted activation? | CRD? | connect-ng PR? |
|---|---|---|---|---|---|---|---|
| 1 | Resolve org creds → regcode ⭐ | +281 / -10 | ~470 | yes (internal) | ✅ | no | no |
| 2 | First-class org cred source | +512 / -27 | ~890 | yes | ✅ | yes | no |
| 3 | Org bootstrap, system steady-state | ~+900 | ~1,700 | **none** | ❌ | yes | yes |
| 4 | Pure org model | ~+1,200 | ~2,290 | none | ❌ | yes | yes |
| 5 | + fleet reporting | ~+2,700 | ~4,000 | none | ❌ | yes | yes |

The split is clean: **counted + small ⇒ 1/2** (regcode stays internally); **regcode-free ⇒ 3/4/5**
(activation uncounted). Pick based on which axis matters more.

---

## 5. Non-code impacts

| Area | Impact | Options |
|---|---|---|
| **Rancher Helm chart** | `registration.regCode` → add `orgUsername`/`orgPassword` (or existing-secret ref). | 1–5 |
| **Rancher UI** (Registration) | Single field → mode toggle + two fields. Under 3–5, `registrationExpiresAt`/`registeredProduct` become inferred (accuracy note). | 1–5 |
| **Rancher SCC deployer** | Learns the new entrypoint-Secret keys. | 1–5 |
| **Docs** `ext/scc-registration.adoc` | Add "Organization → Credentials" alternative to "Obtain Registration Code". | 1–5 |
| **Security review** | Org creds expose the whole catalogue, **every regcode**, and system deletion — a materially different threat model. | 1–5 |
| **SCC team** | Confirm counting semantics you rely on; for 3–5, the `RMT`/`HOST-SYSTEM` header analogue and uncounted-activation expectations. | 3–5 |
| **RBAC / telemetry** | No change. | 1–5 |

---

## 6. Offline mode — unaffected (verified)

Offline is already regcode-free: `Register()` builds a `registration.OfflineRequest` locally (no
network/auth); `Activate()`/`Keepalive()` validate the SCC certificate locally.
`extractRegistrationParamsFromSecret` only enforces `regCode` when `mode == online`, so adding
org-credential input needs **no offline-side change** — only the name/content-hash inputs must include
the new keys (accounted for in Option 1).
