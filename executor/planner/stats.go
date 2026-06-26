package planner

import (
	"aurasql/core"
)

// CardinalityEstimator estimates row counts for operations.
type CardinalityEstimator struct {
	stats map[string]core.TableStats
}

func NewCardinalityEstimator(stats map[string]core.TableStats) *CardinalityEstimator {
	return &CardinalityEstimator{stats: stats}
}

// EstimateSelectivity estimates how many rows pass a predicate.
func (ce *CardinalityEstimator) EstimateSelectivity(table string, pred core.Expr) float64 {
	switch p := pred.(type) {
	case *core.BinaryExpr:
		return ce.estimateBinarySelectivity(table, p)
	default:
		return 1.0 // Unknown predicate: assume all rows pass
	}
}

func (ce *CardinalityEstimator) estimateBinarySelectivity(table string, pred *core.BinaryExpr) float64 {
	// Get column name from left side
	colRef, ok := pred.Left.(*core.ColumnRef)
	if !ok {
		return 0.5 // Unknown: assume 50%
	}

	colStats, exists := ce.stats[table].Columns[colRef.Name]
	if !exists {
		return 0.5 // No stats: assume 50%
	}

	switch pred.Op {
	case core.OpEq:
		// Equality: 1 / distinct_count
		if colStats.DistinctCount > 0 {
			return 1.0 / float64(colStats.DistinctCount)
		}
		return 0.5

	case core.OpNe:
		// Inequality: 1 - (1 / distinct_count)
		if colStats.DistinctCount > 0 {
			return 1.0 - (1.0 / float64(colStats.DistinctCount))
		}
		return 0.5

	case core.OpLt, core.OpLe, core.OpGt, core.OpGe:
		// Range: fraction between min and max
		if colStats.Min.Null || colStats.Max.Null {
			return 1.0 / 3.0 // Default for range
		}
		// Estimate based on range fraction
		// Simplified: assume uniform distribution
		return 0.333 // ~1/3 for range predicates

	case core.OpAnd:
		// AND: multiply selectivities
		leftSel := ce.estimateBinarySelectivity(table, &core.BinaryExpr{
			Op:   core.OpEq,
			Left: pred.Left,
		})
		rightSel := ce.estimateBinarySelectivity(table, &core.BinaryExpr{
			Op:   core.OpEq,
			Left: pred.Right,
		})
		return leftSel * rightSel

	case core.OpOr:
		// OR: s1 + s2 - s1*s2
		leftSel := ce.estimateBinarySelectivity(table, &core.BinaryExpr{
			Op:   core.OpEq,
			Left: pred.Left,
		})
		rightSel := ce.estimateBinarySelectivity(table, &core.BinaryExpr{
			Op:   core.OpEq,
			Left: pred.Right,
		})
		return leftSel + rightSel - leftSel*rightSel

	default:
		return 0.5
	}
}

// EstimateJoinSize estimates rows produced by a join.
func (ce *CardinalityEstimator) EstimateJoinSize(leftTable, rightTable string, on core.Expr) int64 {
	leftStats := ce.stats[leftTable]
	rightStats := ce.stats[rightTable]

	// Extract join column from ON clause
	bin, ok := on.(*core.BinaryExpr)
	if !ok {
		// No stats: assume Cartesian product
		return leftStats.RowCount * rightStats.RowCount
	}

	// Find the join column
	var joinCol string
	switch left := bin.Left.(type) {
	case *core.ColumnRef:
		joinCol = left.Name
	default:
		return leftStats.RowCount * rightStats.RowCount
	}

	// Get distinct counts for join column
	leftDistinct := int64(1)
	if leftColStats, ok := leftStats.Columns[joinCol]; ok && leftColStats.DistinctCount > 0 {
		leftDistinct = leftColStats.DistinctCount
	}

	rightDistinct := int64(1)
	if rightColStats, ok := rightStats.Columns[joinCol]; ok && rightColStats.DistinctCount > 0 {
		rightDistinct = rightColStats.DistinctCount
	}

	// Join size formula: |R| * |S| / max(Distinct(R.x), Distinct(S.y))
	maxDistinct := leftDistinct
	if rightDistinct > maxDistinct {
		maxDistinct = rightDistinct
	}

	if maxDistinct == 0 {
		return leftStats.RowCount * rightStats.RowCount
	}

	return (leftStats.RowCount * rightStats.RowCount) / maxDistinct
}
