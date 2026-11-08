package assessors

// WithIDs makes the service give the providers it registers these ids, in
// order (tests only).
func WithIDs(s *ProviderService, ids ...string) {
	s.newID = func() (string, error) {
		id := ids[0]
		ids = ids[1:]
		return id, nil
	}
}
