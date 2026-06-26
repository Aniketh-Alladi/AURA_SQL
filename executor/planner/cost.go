package planner

// import "aurasql/core"  // ← REMOVE THIS LINE - not used

// CostModel assigns numeric costs to operations.
type CostModel struct {
	// Cost per row for sequential scan (in arbitrary units)
	SeqScanCostPerRow float64
	// Cost per row for index scan
	IndexScanCostPerRow float64
	// Cost per row for join (nested loop)
	JoinCostPerRow float64
}

func NewCostModel() *CostModel {
	return &CostModel{
		SeqScanCostPerRow:   1.0,
		IndexScanCostPerRow: 0.05, // Index scan is faster
		JoinCostPerRow:      1.0,
	}
}

// CostPlan calculates the cost of a plan.
func (cm *CostModel) CostPlan(plan *Plan) float64 {
	switch plan.Type {
	case PlanScan:
		return cm.costScan(plan)
	case PlanIndexScan:
		return cm.costIndexScan(plan)
	case PlanJoin:
		return cm.costJoin(plan)
	case PlanFilter:
		return cm.costFilter(plan)
	case PlanProject:
		return cm.costProject(plan)
	default:
		return 0
	}
}

func (cm *CostModel) costScan(plan *Plan) float64 {
	return float64(plan.RowCount) * cm.SeqScanCostPerRow
}

func (cm *CostModel) costIndexScan(plan *Plan) float64 {
	return float64(plan.RowCount) * cm.IndexScanCostPerRow
}

func (cm *CostModel) costJoin(plan *Plan) float64 {
	// Cost = cost(outer) + outerRows * cost(inner access)
	outerCost := cm.CostPlan(plan.Left)
	innerCost := cm.CostPlan(plan.Right)
	return outerCost + float64(plan.Left.RowCount)*innerCost
}

func (cm *CostModel) costFilter(plan *Plan) float64 {
	// Filter cost is just the cost of the child
	return cm.CostPlan(plan.Child)
}

func (cm *CostModel) costProject(plan *Plan) float64 {
	// Project cost is just the cost of the child
	return cm.CostPlan(plan.Child)
}