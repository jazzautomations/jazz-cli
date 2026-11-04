package main

// HACK: sleep until the port is free; replace with a real wait.
func main() {
	// XXX this leaks the handle
	_ = open() /* BUG: nil on windows */
}
