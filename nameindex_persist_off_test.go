package main

// The on-disk name index is off in tests (every test root would leave an
// .idx in the developer's data dir); TestNiIdx* turn it on with a temp home.
func init() { niPersist = false }
