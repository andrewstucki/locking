# Locking

This repo provides some locking primitives for use with multi-cluster Kubernetes operators. It provides 3 current implementations:

1. A basic lock implementation for single operators based on using the Kubernetes lease API
2. A multi-cluster lock implementation that attempts to acquire multiple leases using the lease API in cases where the amount of locks that are being acquired is small.
3. A raft-based lock implementation that requires operators knowing about each other.

*NOTE:* This software is not production-ready and is meant to demonstrate conceptually how a fleet of multi-cluster operators can be run in disparate clusters and still function.

## Future Ideas

Modify the multi-lock implementation to not require obtaining all locks, but rather a quorum of locks.