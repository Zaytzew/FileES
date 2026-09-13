package ipcserver

import contract "filees/pkg/contract/v1"

func (s *Server) SetMemorySafety(status contract.MemorySafetyStatus) {
	s.mu.Lock()
	s.memorySafety = &status
	s.mu.Unlock()
}

func (s *Server) MemorySafety() *contract.MemorySafetyStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.memorySafety == nil {
		return nil
	}
	value := *s.memorySafety
	return &value
}
