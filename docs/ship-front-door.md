# Ship front door — spyder as the fastlane + secrets principal

Laptop-only store shipping for **Squz** and **Minicades**. Spyder is the
only process that opens the keychain and the only process that launches
fastlane. Build systems (ge Make, MinicadesKit / Unity) stay the
orchestrators: they decide *what* to build and *which* lane to run.
They reach fastlane **through spyder**. Spyder does not reach them.

Status: implemented in-tree (2026-08-25) under 🎯T133 — codesign principal,
envelope, clipboard import with optional live ASC/Play verify,
`secret mint --kind play-upload` (squz), `spyder fastlane` wrap with
lane classes / confirm / dry-run, and `~/.spyder/ship-audit/` JSONL +
reflection stubs. Residual: Homebrew bottle codesign tracked as 🎯T134 (unsigned bottles
correctly refuse secrets until signed). Consumer Make wiring is **not**
in this repo (see §8 follow-ups: ge 🎯T183, MinicadesKit 🎯T12).

## 1. Why this exists

Today secrets and ship mechanics are split by accident of history:

| Portfolio | Engine | Apple sign | Apple upload | Android sign | Android upload |
|---|---|---|---|---|---|
| Squz | ge (Make + fastlane) | match + `squz/certs` + `MATCH_PASSWORD` in the shell / 1Password | `pilot` / `deliver` + ASC `.p8` on disk | keychain envelope (in progress) or nothing | Console drag |
| Minicades | Unity + MinicadesKit Make | Xcode Automatic | Transporter / `altool` | JKS on disk + keychain *passwords* | Console drag |

The wanted end state: one **codesigned** spyder binary is the keychain
principal (same reason `storectl` is compiled Go, not `security` from
Make). Build tools call `spyder fastlane …` (or an equivalent plugin
that is just a client of that binary). Fastlane never sees a
long-lived environment, MCP, or chat transcript.

Sharing keys with other developers is **out of scope**. 1Password is
not required.

## 2. Non-negotiables

1. **Spyder does not know how games build.** No Unity `-executeMethod`,
   no ge worktrees, no `version.properties`, no Transporter SQLite, no
   `ship-alpha` semantics inside spyder. Those stay in ge /
   MinicadesKit.
2. **Build tools do not invoke `bundle exec fastlane` or `security`.**
   They invoke spyder. Spyder execs fastlane as a child and injects
   secrets only into that process.
3. **No GitHub Actions (or any CI runner) ship path.** Everything
   happens on the developer Mac. Existing workflow templates that
   assume org secrets on a runner are not part of this design.
4. **No secret values on MCP, REST `:3030`, or `app_exec`.** CLI
   (and the fastlane child) only. A `secret_get` verb on the daemon
   is a defect.
5. **Agents control named recipes in the consuming repo**
   (`make ship-alpha`, `make playstore`, …), never raw fastlane
   actions and never spyder secret contents.
6. **Store presence, SKUs, prices, questionnaires, and review
   remain human.** Fastlane may upload binaries and listing *files*
   the consumer already committed; it does not create accounts or
   pass review.
7. **Two Apple teams, two Play organisations.** They are not
   interchangeable.

## 3. Identity map

| Studio slug (`STUDIO`) | Apple team (name) | Apple Team ID | Play developer account | Match git repo |
|---|---|---|---|---|
| `squz` | Squz Pty Ltd | `SWA3H3N7TW` | `5616010429813037862` | `squz/certs` |
| `minicades` | Minicades Mobile Pty Ltd | `R4D5JQEEE2` | (Minicades Play org — record at import) | `minicades/certs` (when match is adopted) |

Personal Team and “None” are **not** ship identities.

Xcode Automatic + “Xcode Managed Profile” remains valid for
**development** installs on both portfolios. Ship signing is match
(Apple Distribution) once that studio has a certs repo.

Game metadata (package / bundle id, Play *app* id, `STUDIO`, listing
copy, SKUs) stays in the game repo. Spyder stores only secrets,
keyed by studio slug.

## 4. Process model

```
human / agent
    → make ship-*  |  make testflight  |  make playstore   (consumer)
        → xcodebuild / Unity batchmode / gradle            (consumer)
        → spyder fastlane <action> [args]                  (front door)
            → SecItem*  (this binary only)
            → exec fastlane with env in the child only
                → match | gym | pilot | deliver | supply
                → codesign  (Apple identity match imported)
```

Spyder is **not** `spyder ship -- make …`. That would bake consumer
recipes into the control plane and invert the dependency.

`gym` is a fastlane action, so a ge consumer that already builds the
`.xcodeproj` itself may call `spyder fastlane pilot` with a
pre-built IPA. A consumer that wants gym inside fastlane may call
`spyder fastlane build_ipa`. Spyder does not care which.

Unity: MinicadesKit keeps `make ios` / `make android` as today.
When it needs match, `altool`, or later `pilot`/`supply`, it calls
spyder for those steps only. Spyder never launches Unity.

## 5. The spyder binary

### 5.1 Codesign (minimise keychain prompts)

Keychain ACLs and TCC bind to the **code signature** of the
executable that called `SecItem*`. Unsigned `bin/spyder` from a
dev tree, a Homebrew bottle with a different CDHash, and
`/usr/bin/security` are three different principals — each prompts.

Requirements:

- Sign every shipped and every local-dev `spyder` used for secrets
  with a **stable Developer ID Application** (or a dedicated
  Development cert used only for this binary).
- Stable bundle id: `com.marcelocantos.spyder`.
- Hardened runtime. Keychain-access-groups entitlement if we use
  an access group; otherwise a generic-password item ACL’d to this
  designated requirement.
- Homebrew bottles and `make install` must **not** ad-hoc re-sign
  in a way that changes the designated requirement. A rebuild that
  keeps the same signing identity should not re-prompt.
- Do **not** call `/usr/bin/security` for studio secrets. Port
  `storectl`’s cgo `SecItem*` path into spyder (`internal/ship`).
- Local: `make build && make sign` (or `SPYDER_CODESIGN_IDENTITY=…`).
  Unsigned binaries refuse `secret` / `fastlane` unless
  `SPYDER_ALLOW_UNSIGNED_SECRETS=1` (hermetic tests only).
- `make sign` prefers Developer ID Application, else Apple
  Development; forces `--identifier com.marcelocantos.spyder`.

`codesign` / `xcodebuild` still access the **Apple Distribution
identity** match imports. That is a different keychain class
(identity, not our generic-password envelope). Unavoidable and
unchanged.

### 5.2 CLI surface (no daemon)

These commands talk to the keychain in-process. They must work
with the daemon down.

```
spyder secret status  --studio squz|minicades
spyder secret import  --studio …     # clipboard absorb; see §6
spyder secret mint    --studio … --kind play-upload
spyder secret missing --studio … --for match|pilot|deliver|supply|firebase

spyder fastlane <fastlane-args...>   # cwd = consumer; see §7
    [--studio squz|minicades]
    [--confirm]                      # required for irreversible class
    [--dry-run]                      # rehearsal; see §9
```

`--studio` defaults to `STUDIO` in the environment (game metadata)
or a `fastlane/Matchfile` / spyder sidecar the consumer already
has. Spyder may *read* `STUDIO` / Team ID to pick the envelope.
It may not *invent* build steps from them.

`spyder secret get` that prints a raw secret to stdout is allowed
for debugging behind an explicit `--print` and a tty check. It is
not how lanes run. Agents must not be taught `--print`.

### 5.3 What is not a spyder API

- MCP / REST `secret_*` verbs
- `spyder run -- fastlane` (that flag is device reservation)
- A catalogue of `ship-alpha` / `playstore` inside spyder
- GitHub `secrets.*` injection

## 6. Secrets

### 6.1 Storage

One generic-password item **per studio** (one ACL prompt per
studio, not per kind). JSON envelope, versioned.

```
service:  spyder.studio
account:  squz | minicades
```

Fields (all optional until imported/minted):

| Kind | Contents | Used by |
|---|---|---|
| `match_password` | passphrase | `match` |
| `asc` | `issuer_id`, `key_id`, `p8` PEM | `pilot`, `deliver`, `register_devices`, match rotate |
| `play_upload` | PKCS12 + alias + unlock password (generated) | Gradle / Unity signing via a **wiped temp file** spyder writes for the child only |
| `play_service_account` | JSON | `supply` |
| `firebase_admin` | Admin SDK JSON (prod) | future spyder telemetry; Crashlytics / symbol upload |
| `firebase_admin_dev` | Admin SDK JSON (dev project) | same, `FIREBASE_ENV=dev` |
| `iap_testers` | optional later; not required for v1 | license / sandbox notes |

`google-services.json` / `GoogleService-Info.plist` in a game
repo are **identifiers**, not this envelope, unless a consumer
has been treating them as secret.

Play upload: one private key per **Play organisation**. Squz uses
one alias (`upload`). Minicades keeps the existing **multi-alias**
JKS inside the same `play_upload` blob (do not reset Play upload
certs). Spyder’s mint for minicades is “import existing JKS from
clipboard / once-from-disk then delete the file”, not “generate
nine new aliases.”

ASC `.p8` is per-engineer in practice (Apple’s download-once
key). It still lives in the studio envelope on this laptop. A
second person minting their own key is future sharing work.

### 6.2 Acquisition (no insecure transit)

Goal: secrets move **Console → clipboard → spyder memory →
keychain**, never through chat, gist, `.zshrc`, repo files, or
iMessage history.

Lift `storectl import`:

1. Human opens ASC / Play / Firebase console (or Fastlane match
   first-run passphrase prompt we generate ourselves).
2. `spyder secret import --studio squz` watches the clipboard.
3. Each recognised paste is absorbed, **clipboard cleared
   immediately**, surrounding chat text ignored.
4. Recognised shapes (same as storectl, plus):
   - `IssuerID:` / `KeyID:` + `-----BEGIN PRIVATE KEY-----`
   - Play service-account JSON (`"type":"service_account"`)
   - Firebase Admin JSON (same shape; distinguished by
     `client_email` / project id the human confirms)
   - PKCS12 / JKS bytes (base64) for Play upload import
   - A match passphrase the command **generates** and displays
     once, then stores — do not paste `MATCH_PASSWORD` from
     1Password if we are minting a new certs repo. Existing
     `squz/certs` passphrase: human copies from wherever it
     already lives, once, into this import (last 1Password use).
5. Live verify against ASC / Play / Firebase **read-only**
   endpoints before the item is considered complete (`storectl`
   already does this; keep `-no-verify` for hermetic tests).
6. `play-upload` **mint** generates the key pair in memory /
   a 0600 temp that is wiped in the same process. Never a
   persistent `~/.config/…/play-upload.jks`.

Insecure (refuse):

- Env vars in the parent shell as the source of truth
- Files left in `/tmp` after the command exits
- Printing PEMs in MCP tool results
- Email / Slack as the acquisition path

### 6.3 Injection into fastlane (child only)

`spyder fastlane` loads the studio envelope, then execs
`bundle exec fastlane …` with a **cleared, explicit env**:

- `MATCH_PASSWORD`
- `APP_STORE_CONNECT_API_KEY_KEY_ID` / `_ISSUER_ID`
- `APP_STORE_CONNECT_API_KEY_KEY_PATH` → 0600 temp `.p8`, wiped
  in `defer` after fastlane exits
- `ANDROID_KEYSTORE_PATH` / passwords → 0600 temp PKCS12, wiped
- `SUPPLY_JSON_KEY` or equivalent temp for `supply`
- Pass through consumer-needed non-secrets: `APP_ID`,
  `SHIP_SCHEME`, `PATH`, `HOME`, `TMPDIR`, `STUDIO`

The parent Make process never has `MATCH_PASSWORD`. Fastlane’s
process does — that is match’s contract and cannot be removed
without forking match.

A tiny Fastfile helper the consumer *may* `import` is acceptable
if and only if it shells to `spyder fastlane` or asks spyder to
materialise temps. It must not call `security` or read
`~/.zshrc`.

## 7. Fastlane through spyder

Spyder runs whatever fastlane action the consumer names. It
applies a **lane class**, not a consumer recipe name.

| Class | Examples | Gate |
|---|---|---|
| `read` | `sync_certs` (readonly match), `precheck` | none |
| `build` | `gym`, `build_ipa` | none (no store mutation) |
| `test_publish` | `pilot` internal, `supply` track internal/alpha/beta | audit only |
| `prod_publish` | `deliver` submit, `supply` production, `pilot` external if treated as store-visible | `--confirm` |
| `irreversible` | `match nuke`, upload-key reset, `deliver` metadata overwrite if consumer opts in | `--confirm` + explicit action allowlist |

Spyder does not decide “this is ge alpha.” The consumer’s
`make ship-release` is responsible for `CONFIRM=1` *and* must
pass `--confirm` into `spyder fastlane deliver …`. Either gate
failing is a hard stop.

`--dry-run`: spyder resolves secrets, writes temps, prints the
exact argv and **redacted** env keys (not values), does not exec
fastlane (or execs with fastlane’s own `--dry-run` where the
action supports it). Used in CI-of-the-laptop (`make test`) and
before a first live `test_publish`.

Match file / team: the **consumer** Matchfile already contains
`git_url` + `team_id`. Spyder does not pick `squz/certs` vs
`minicades/certs`. Two teams ⇒ two Matchfiles in two portfolios.
A wrong `--studio` vs Matchfile `team_id` is a preflight error
(`spyder secret missing` / a check that envelope studio matches
`team_id`).

## 8. Consumer contracts (not implemented in spyder)

Spyder ships the CLI contract below. **ge** and **MinicadesKit** keep
their Make / Unity orchestration and switch callers when ready.
This section is the stable API those repos implement against — spyder
does not vendor their Fastfiles or execute their recipes.

### 8.1 ge

- Keep `make ship-alpha` / `ship-beta` / `ship-release`,
  worktrees, version MAX(rev-list, Transporter), `/ge:ship`.
- Replace `bundle exec fastlane …` and required
  `MATCH_PASSWORD` in the parent with:

  ```bash
  spyder fastlane --studio squz [--confirm] [--dry-run] -- <action> [args…]
  # or: STUDIO=squz spyder fastlane [--confirm] -- <action> …
  ```

- Preflight: `spyder secret missing --studio squz --for match|pilot|deliver|supply|firebase`
- Drop `MATCH_PASSWORD` from `~/.zshrc` onboarding.
- Android: `make android-bundle` asks spyder to materialise
  the upload PKCS12 for Gradle (or `spyder fastlane` a supply
  lane after the AAB exists).
- Fastfile stays in `ge/fastlane/`; spyder does not vendor it.

**Follow-up (filed outside spyder):** ge 🎯T183 — “ship Make invokes
`spyder fastlane` / `spyder secret missing`; parent env has no
`MATCH_PASSWORD`.” Ledger: `squz/ge` bullseye.yaml.

### 8.2 MinicadesKit / Unity

- Keep Unity batchmode, `podfile-fixup`, preflight dirty-tree
  checks, `docs/releases.yaml`.
- Signing passwords: stop reading `android-keystore` via
  `security` in Make. Call spyder to materialise the JKS for
  the Unity child, then wipe (via `spyder fastlane` or a future
  materialise helper that uses the same envelope).
- Apple ship: either keep Automatic+altool **via**
  `spyder fastlane` / a spyder `altool` wrap that uses the
  ASC envelope, or adopt match + `minicades/certs` on team
  `R4D5JQEEE2`. Dev stays Automatic.
- Do not move Unity executeMethod names into spyder.
- Retire or stub `storectl` so it execs `spyder` (or delete after
  callers switch). Do **not** keep `storectl serve`.

**Follow-up (filed outside spyder):** MinicadesKit 🎯T12 — “Make /
storectl callers use spyder secret + fastlane; no `/usr/bin/security`
for studio secrets.” Ledger: `minicadesmobile/MinicadesKit` bullseye.yaml.

### 8.3 Agent

`/ge:ship` and any future `/kit:ship` still dispatch **make**.
They never call `spyder secret` with `--print` and never call
fastlane.

## 9. Testing methodology (irreversible operations)

Live `match nuke`, Play upload-key reset, `deliver` submit, and
`supply` production **must not** appear in the automated suite
against real apps.

### 9.1 Hermetic (every commit)

- Clipboard parser: fixtures of ASC / Play / Firebase pastes
  mixed with chat; assert absorb + clipboard-clear calls
  (mock pasteboard).
- Envelope JSON schema + forward-compatible unknown fields.
- `spyder fastlane --dry-run`: fake `bundle` on `PATH` that
  records argv + which env keys were set (not values) + that
  parent env lacked `MATCH_PASSWORD`.
- Temp PKCS12 / `.p8` life: created 0600, path passed in,
  gone after process exit (even on SIGTERM).
- Lane class: `deliver --submit_for_review` without
  `--confirm` exits non-zero; `sync_certs` does not need it.
- Studio / team_id mismatch fixture.
- Unsigned binary refuses `secret import` in production
  builds (`SPYDER_ALLOW_UNSIGNED_SECRETS=1` for unit tests
  only).

### 9.2 Rehearsal (laptop, no store mutation)

- `spyder secret status` / `missing` against the real
  keychain (interactive, not CI).
- `--dry-run` of each consumer’s make target once.
- Read-only verify (`asc get /v1/apps`, Play list) using
  stored creds — same as `storectl status`.

### 9.3 First live (test_publish only)

- Dedicated **scratch** bundle id / Play package if we need
  to exercise `rotate_certs` or first AAB. Not MultiMaze
  production, not a live Minicades title.
- First real upload: TestFlight **internal** or Play
  **internal testing** only.
- Production (`deliver` review, Play production) is a
  human-gated real run, not a test.

### 9.4 What we never automate against production

- `match nuke`
- Play “Request upload key reset”
- Deleting IAP products
- Overwriting live listing copy unless the consumer
  explicitly opted into metadata lanes

Those commands exist. Their tests are hermetic argv/class
checks plus a written rehearsal checklist.

## 10. Logging, audit, self-reflection

### 10.1 What is recorded

Each `spyder fastlane` (and each `secret import` / `mint`)
appends a JSON line to `~/.spyder/ship-audit/YYYY-MM-DD.jsonl`
and a sibling human file `~/.spyder/ship-audit/YYYY-MM-DD.md`
section.

Record:

- timestamp, hostname, spyder version, codesign CDHash
- studio slug, lane class, fastlane argv (no env values)
- consumer `cwd`, git SHA + dirty flag (read-only `git`)
- secret kinds **present** (boolean map), never values
- fingerprints: upload-key SHA-256, ASC key id (not PEM),
  Play SA client_email
- child exit code, duration
- store identifiers returned if fastlane prints them
  (TF build number, Play versionCode) — parsed, not the log
  dump
- `--dry-run` / `--confirm` flags
- path to a redacted child stdout/stderr capture
  (`~/.spyder/ship-audit/runs/<id>.log`) with a scrubber
  for PEM, `-----BEGIN`, `password=`, `MATCH_PASSWORD=`

### 10.2 What is never recorded

Secret values, PKCS12 bytes, `.p8` PEM, full service-account
JSON, clipboard contents, MCP transcripts.

### 10.3 Self-reflection (every real run)

After a non-dry-run with class `test_publish` or
`prod_publish`, spyder writes
`~/.spyder/ship-audit/runs/<id>-reflect.md` with a stub:

```
# Run <id>
Lane class:
Argv:
What we expected:
What happened (fill in):
Surprises:
Change the consumer recipe? (yes/no — where)
Change spyder? (yes/no — file a target)
```

The next agent session that ships should `open` the latest
unfilled stub before another `prod_publish`. This is the
improvement loop: recipes and lane-class rules get sharper
only if every live run leaves a residue.

Optional later: richer filtering. `spyder ship-audit` already lists
unfilled reflection stubs under `~/.spyder/ship-audit/runs/`.

### 10.4 Telemetry (Firebase)

Spyder’s future device/session telemetry uses
`firebase_admin` / `firebase_admin_dev` from the same
envelope, in-process, same principal. It does not get a
second keychain item. Ship audit and product telemetry are
different streams: audit stays **local files**; Firebase
is opt-in and must not receive secret values or PEMs.

## 11. storectl

`storectl` is the prototype of §5–§6 (cgo keychain, clipboard
import, ASC/Play verify). **Absorbed into spyder** (`internal/ship`):

- One principal (codesigned spyder), not two
- Clipboard detect/absorb → `spyder secret import`
- SecItem* keychain → `spyder.studio` envelope (not `storectl` service)
- `storectl serve` localhost proxy is **not** carried over
  (it is a secret-over-HTTP footgun). If something needs
  an ASC JWT, `spyder secret token asc --studio …` (future) prints
  a short-lived token to a tty, or writes it to a temp
  for a named child, same as fastlane wrap.
- MinicadesKit should stub or delete `scripts/storectl` after
  callers switch (consumer follow-up; not implemented here).

## 12. Phasing

1. Codesign + SecItem envelope + `status` / `import` / `mint`
   (absorb storectl tests).
2. `spyder fastlane --dry-run` + audit jsonl + hermetic suite.
3. ge: one consumer (`multimaze2`) `sync_certs` + `pilot`
   internal through spyder.
4. Minicades: materialise Play JKS for `make android`; ASC
   for `altool` / later match.
5. Firebase kinds + spyder telemetry reader.
6. Reflections required before second `prod_publish`.

## 13. Defaults (settled here)

| Fork | Default |
|---|---|
| Match repos | **Two** (`squz/certs`, `minicades/certs`) — two Apple teams |
| Minicades Play key | **Import existing multi-alias JKS**; do not rotate |
| CI runners | **None** |
| Orchestrator | **Consumer Make**; spyder is the fastlane/secrets door |
| storectl | **Absorb** into spyder |
| Production binary | Consumer decides rebuild vs promote; spyder runs the action it is given |
| Metadata upload | Off until a consumer passes an explicit deliver/supply metadata lane |

## 14. Related

- ge: `docs/release-setup.md`, `docs/android-release.md`,
  `fastlane/Fastfile`
- MinicadesKit: `.build/Modules.mk`, `storectl`
- This file is the spyder-side contract. Consumer wiring is
  follow-up in those repos, not extra assumptions here.
