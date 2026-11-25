# Shared infrastructure

The nine common Go packages in this module are compiled into both applications. This is source reuse, not a third service or an independent runtime. Keep the library and acquisition modules' dependency selection independent (`GOWORK=off`); their local replace directives point here. Platform-specific source and embedded web assets stay in this module. Application-specific UI, data ownership and network permissions stay with each engine. See [MEBOORU.md](../MEBOORU.md) for the authoritative roadmap and acceptance policy.
