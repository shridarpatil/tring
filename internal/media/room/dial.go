package room

// DialFailure carries a transport-neutral, retryable carrier disposition.
// Unclassified errors are deliberately not automatically retried by campaigns.
type DialFailure struct {
	Outcome string
	Err     error
}

func (e *DialFailure) Error() string { return e.Outcome + ": " + e.Err.Error() }
func (e *DialFailure) Unwrap() error { return e.Err }
