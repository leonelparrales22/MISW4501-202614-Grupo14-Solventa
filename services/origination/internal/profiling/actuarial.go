package profiling

import "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"

// ScoreFormulaVersion identifies the scoring rule below, so a change of
// weights or formula can be told apart in logs and later in audit records.
const ScoreFormulaVersion = "v1"

// DefaultWeights are the provisional actuarial weights of each source
// (HU-W1.2-02), to be replaced when the actuarial team defines the formula:
// Open Finance 0.5, Open Data 0.3, KYC 0.2.
func DefaultWeights() map[string]float64 {
	return map[string]float64{
		domain.ExternalSourceOpenFinance: 0.5,
		domain.ExternalSourceOpenData:    0.3,
		domain.ExternalSourceKYC:         0.2,
	}
}

// CombineWeighted builds one profile from the signals that arrived: the
// weighted average of their scores with the weights renormalized over the
// sources present, the category derived from that score and the calculation
// date of the highest-priority source present. names[i] is the source of
// arrived[i]. A source without a positive weight does not count; when none
// does, the plain average is used so a profile is always produced.
func CombineWeighted(customerID string, arrived []domain.RiskProfile, names []string, weights map[string]float64) domain.RiskProfile {
	var sum, total float64
	for i, p := range arrived {
		if w := weights[names[i]]; w > 0 {
			sum += w * p.Score
			total += w
		}
	}
	if total == 0 {
		return CombineProvisional(customerID, arrived)
	}
	score := sum / total
	return domain.RiskProfile{
		CustomerID:   customerID,
		Score:        score,
		Category:     domain.CategoryForScore(score),
		CalculatedAt: arrived[0].CalculatedAt,
	}
}
