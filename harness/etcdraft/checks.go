package etcdraft

import "github.com/hmdsefi/faultline/harness/etcdraft/internal/oracle"

// invariantNames are the ETC-120 invariants in registration order.
var invariantNames = []string{
	oracle.Harness,
	oracle.ElectionSafety,
	oracle.StateMachineSafety,
	oracle.DurableState,
	oracle.HardStateMonotonic,
	oracle.ReadyContract,
	oracle.ConfigAgreement,
	oracle.LeaderIsVoter,
}

// registerChecks registers the invariants (ETC-120).
func (c *Cluster) registerChecks() {
	for _, name := range invariantNames {
		c.w.Invariant(name, func() error { return c.o.Err(name) })
	}
}
