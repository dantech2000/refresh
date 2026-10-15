package fleettest

// ViewAt is View, and returns the generation of the state that f read. A
// server that stamps what it serves with the generation reads both with one
// call, so no change can come between the state and its number.
func (w *World) ViewAt(f func(s *State)) int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	f(&w.state)
	return w.generation
}
