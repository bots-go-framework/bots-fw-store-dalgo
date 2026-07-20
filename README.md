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
