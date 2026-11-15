package assistance

// WithNewID replaces the generator of ids, so a test can make it fail.
func (s *Service) WithNewID(newID func() (string, error)) *Service {
	s.newID = newID
	return s
}
