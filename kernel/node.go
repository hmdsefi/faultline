package kernel

// NodeID identifies a node. IDs are dense, start at 1, and follow AddNode order. 0 means global.
type NodeID int32
