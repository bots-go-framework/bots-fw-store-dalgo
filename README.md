# bots-fw-store-dalgo

The default DALgo adapter for `bots-fw-store/botsfwstore.StateStore`.

It keeps the established keys and record shapes unchanged:

`botPlatforms/{platform}/botUsers/{platformUser}` and
`botPlatforms/{platform}/bots/{bot}/botChats/{chat}`.

Consequently, adopting this module is a code-composition change, not a database
migration. Provide an `AppUserStore` that knows how the consuming application
creates and loads its own users. Its `PrepareAppUser` phase may idempotently
provision external identity before the retryable callback; its `EnsureAppUser`
phase persists the prepared application record with the supplied transaction.
The adapter commits that record, the platform-user link, and chat together. The
framework itself never sees a DALgo connection or transaction.

## Partial updates

A platform-user document (`botPlatforms/{platform}/botUsers/{user}`) is shared
by every bot of a platform, and different services may model it with different
Go types. To keep them from wiping each other's fields, an operation on an
existing platform user writes only the fields it owns, with DALgo field updates
that only set values:

| Operation | Fields written |
|---|---|
| `EnsureLinked`, linking an existing platform user | `appUserID`, `botIDs`, `dtUpdated` (and `dtCreated` only when it was missing) |
| `SetPlatformUserAccessGranted` | `isAccessGranted`, `botIDs`, `dtUpdated` (and `dtCreated` only when it was missing) |

`Set` / `Insert` is used only for a platform user's first insert. Fields that
the supplied Go type does not model are never touched.

Chat documents live under their bot
(`botPlatforms/{platform}/bots/{bot}/botChats/{chat}`), so two bots never share
one; they are still written whole with `Set`.
