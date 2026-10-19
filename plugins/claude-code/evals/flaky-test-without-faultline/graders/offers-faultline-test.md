---
type: llm
---

PASS if the reply says that faultline cannot run this test or the replication code as it is, because the code starts goroutines and uses `net` (it needs a port to faultline's node API or simulated network first), and offers to write a faultline simulation test of the replication.
FAIL if the reply says or implies that faultline can replay the existing TestReplicasCatchUp from a seed unchanged (for example by setting FAULTLINE_SEED on it), if it does not offer a faultline test, or if it does not mention faultline.
