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

Several services can share these collections with different Go types for the
same document. To keep them from wiping each other's fields, an operation on an
existing document writes only the fields it owns, with DALgo field updates:

| Operation | Fields written |
|---|---|
| `EnsureLinked`, linking an existing platform user | `appUserID`, `botIDs`, `dtUpdated` (and `dtCreated` only when it was missing) |
| `EnsureLinked`, linking an existing chat | `appUserID`, `dtUpdated` (and `dtCreated` only when it was missing) |
| `SetPlatformUserAccessGranted` | `isAccessGranted`, `botIDs`, `dtUpdated` (and `dtCreated` only when it was missing) |
| `SaveChat` on an existing chat | every field the supplied chat type models (an empty `omitempty` field is deleted) |

`Set` / `Insert` is used only for the first insert of a document. Fields that
the supplied Go type does not model are never touched.
