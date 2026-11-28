package acquisitionpolicy

import "time"

// One trusted reader per inherited pipe. Polling is restricted to the
// one-byte activation/readiness/liveness control channels, not media or
// general protocol streams. No goroutine blocks in a read with no data.
const custodyPipeInterval = 20 * time.Millisecond
