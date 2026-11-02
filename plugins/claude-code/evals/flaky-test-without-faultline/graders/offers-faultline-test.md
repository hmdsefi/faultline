---
type: llm
---

PASS if the reply says that faultline cannot replay the existing test or run the replication code as it is, because the code calls `net.Listen` or `net.Dial` itself (it must take its listener and dialer as values, such as faultline's simulated network `p.Net` in goroutine mode, or be ported to faultline's node API), and offers to write a faultline simulation test of the replication.
FAIL if the reply says or implies that faultline can replay the existing TestReplicasCatchUp from a seed unchanged (for example by setting FAULTLINE_SEED on it), if it says that faultline cannot run goroutine code at all, if it does not offer a faultline test, or if it does not mention faultline.
