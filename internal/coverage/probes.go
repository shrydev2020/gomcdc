package coverage

// Probes identifies original-source targets for which probe code was generated.
type Probes struct {
	Decisions        []DecisionID `json:"decisions"`
	ClauseBodies     []ClauseID   `json:"clauseBodies"`
	ClauseSelections []ClauseID   `json:"clauseSelections"`
	NoMatches        []SwitchID   `json:"noMatches"`
}
