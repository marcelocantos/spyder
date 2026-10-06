# Target: Keychain biometry / user-presence ACL

## Why Keychain today

Studio secrets (`spyder.studio` envelopes) are read/written only via in-process
SecItem* in `internal/ship/keychain_darwin.go`. Codesigned spyder is the sole
Security.framework principal; unsigned/ad-hoc builds are refused.

## Prompt friction

SecItemAdd trusts the creating binary's identity. After a resign or CDHash
change, Keychain re-prompts with the **login password** (file-based login
keychain ACL). Touch ID is available on the machine but not offered for these
ACL approvals. Bottle/dev rebuilds renew approval until a stable Canticode
identity exists.

## Recommended change

1. When creating/updating studio secret items, set `kSecAttrAccessControl` from
   `SecAccessControlCreateWithFlags(..., kSecAccessControlUserPresence | …)`
   (evaluate `biometryCurrentSet` vs `userPresence` for passcode fallback).
2. Keep RequireSecretsAccess / codesign gates; biometry complements them — it
   does not replace signature checks.
3. Document that binary upgrades still re-trigger ACL approval until the
   Canticode Apple account signing identity is stable; biometry should turn
   those prompts into Touch ID instead of password entry.
4. Note (same as sentinel): legacy login-keychain access-list prompts ignore
   LAContext; migrating the item to an access-control–protected item is what
   enables Touch ID UI.

## Non-goals

Do not reintroduce `/usr/bin/security` for studio secrets. Do not relax
unsigned-binary refusal.
