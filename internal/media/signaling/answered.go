package signaling

import "github.com/shridarpatil/tring/internal/media/room"

// A visitor's media connects to the IVR before a human answers. Use the
// authenticated agent join to signal answer separately from media readiness.
func (s *Server) notifyVisitorsAnswered(rid room.RoomID) {
	r, ok := s.rooms.Get(rid)
	if !ok {
		return
	}
	participants := r.Participants()
	var visitors []*conn
	s.mu.Lock()
	for _, p := range participants {
		if role, known := s.roles[p.ID()]; known && role == roleVisitor {
			if c := s.conns[p.ID()]; c != nil {
				visitors = append(visitors, c)
			}
		}
	}
	s.mu.Unlock()
	for _, c := range visitors {
		_ = c.send(msg{Type: msgAnswered})
	}
}
