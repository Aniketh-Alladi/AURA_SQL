package planner

import (
	"math"
	"sort"

	"aurasql/core"
)

// Plan represents a query plan.
type Plan struct {
	Type      PlanType
	Table     string
	RowCount  int64
	Cost      float64
	Child     *Plan
	Left      *Plan
	Right     *Plan
	Predicate core.Expr
}

type PlanType int

const (
	PlanScan PlanType = iota
	PlanIndexScan
	PlanJoin
	PlanFilter
	PlanProject
)

// Optimizer performs cost-based optimization.
type Optimizer struct {
	eng         core.StorageEngine
	stats       map[string]core.TableStats
	costModel   *CostModel
	cardinality *CardinalityEstimator
	memo        map[string]*Plan // DP memo: table set -> best plan
}

func NewOptimizer(eng core.StorageEngine) *Optimizer {
	return &Optimizer{
		eng:       eng,
		stats:     make(map[string]core.TableStats),
		costModel: NewCostModel(),
		memo:      make(map[string]*Plan),
	}
}

// Optimize finds the cheapest plan for a SELECT query.
func (o *Optimizer) Optimize(stmt *core.SelectStmt) (*Plan, error) {
	// 1. Collect stats for all tables in the query
	tables := []string{stmt.From}
	for _, join := range stmt.Joins {
		tables = append(tables, join.Table)
	}

	for _, table := range tables {
		stats, ok := o.eng.Stats(table)
		if ok {
			o.stats[table] = stats
		} else {
			// No stats: estimate with default values
			o.stats[table] = core.TableStats{
				RowCount: 1000,
				Columns:  make(map[string]core.ColumnStats),
			}
		}
	}

	o.cardinality = NewCardinalityEstimator(o.stats)

	// 2. Find the best join order using DP
	allTables := make(map[string]bool)
	for _, t := range tables {
		allTables[t] = true
	}

	bestPlan, err := o.findBestJoinOrder(allTables, stmt.Joins)
	if err != nil {
		return nil, err
	}

	// 3. Add filters and projection on top
	if stmt.Where != nil {
		filterPlan := &Plan{
			Type:      PlanFilter,
			Child:     bestPlan,
			RowCount:  int64(float64(bestPlan.RowCount) * o.cardinality.EstimateSelectivity(stmt.From, stmt.Where)),
			Predicate: stmt.Where,
		}
		filterPlan.Cost = o.costModel.CostPlan(filterPlan)
		bestPlan = filterPlan
	}

	// Add projection
	projectPlan := &Plan{
		Type:     PlanProject,
		Child:    bestPlan,
		RowCount: bestPlan.RowCount,
	}
	projectPlan.Cost = o.costModel.CostPlan(projectPlan)
	bestPlan = projectPlan

	return bestPlan, nil
}

// findBestJoinOrder uses DP to find the cheapest join order.
func (o *Optimizer) findBestJoinOrder(tables map[string]bool, joins []core.JoinClause) (*Plan, error) {
	// Convert tables to slice for consistent iteration
	tableList := make([]string, 0, len(tables))
	for t := range tables {
		tableList = append(tableList, t)
	}

	// Build join predicate map
	joinPredicates := make(map[string]core.Expr) // "table1,table2" -> predicate
	for _, join := range joins {
		key := join.Table // Simplified: assumes all joins are with the FROM table
		// In a real implementation, this would track table pairs
		joinPredicates[key] = join.On
	}

	// DP: for each subset size, build best plans
	n := len(tableList)
	if n == 1 {
		// Base case: single table
		return o.bestAccessPath(tableList[0]), nil
	}

	// Memoization: subset -> plan
	memo := make(map[string]*Plan)

	// For each subset size
	for size := 1; size <= n; size++ {
		// Generate all subsets of this size
		subsets := o.generateSubsets(tableList, size)
		for _, subset := range subsets {
			subsetKey := o.subsetKey(subset)
			if size == 1 {
				// Single table: best access path
				memo[subsetKey] = o.bestAccessPath(subset[0])
			} else {
				// Try joining this subset with another
				bestPlan := o.findBestJoinForSubset(subset, memo, joins)
				if bestPlan != nil {
					memo[subsetKey] = bestPlan
				}
			}
		}
	}

	// Return the plan for all tables
	allKey := o.subsetKey(tableList)
	if plan, ok := memo[allKey]; ok {
		return plan, nil
	}

	// Fallback: left-deep in original order
	return o.buildLeftDeepPlan(tableList, joins), nil
}

// bestAccessPath chooses between seq scan and index scan.
func (o *Optimizer) bestAccessPath(table string) *Plan {
	stats := o.stats[table]

	// Check if there are any indexes with selective predicates
	// For now, always use seq scan
	plan := &Plan{
		Type:     PlanScan,
		Table:    table,
		RowCount: stats.RowCount,
	}
	plan.Cost = o.costModel.CostPlan(plan)
	return plan
}

// findBestJoinForSubset finds the cheapest way to join a subset.
func (o *Optimizer) findBestJoinForSubset(subset []string, memo map[string]*Plan, joins []core.JoinClause) *Plan {
	// Try each split of the subset into two parts
	n := len(subset)
	if n < 2 {
		return nil
	}

	var bestPlan *Plan
	bestCost := math.MaxFloat64

	// Try all ways to split the subset
	// For simplicity, try removing one table at a time (left-deep)
	for i := 0; i < n; i++ {
		// Get the right table
		rightTable := subset[i]
		rightKey := o.subsetKey([]string{rightTable})
		rightPlan, ok := memo[rightKey]
		if !ok {
			continue
		}

		// Get the left subset (all except rightTable)
		leftSubset := make([]string, 0, n-1)
		for j := 0; j < n; j++ {
			if j != i {
				leftSubset = append(leftSubset, subset[j])
			}
		}
		leftKey := o.subsetKey(leftSubset)
		leftPlan, ok := memo[leftKey]
		if !ok {
			continue
		}

		// Try each join predicate involving rightTable
		for _, join := range joins {
			if join.Table == rightTable {
				joinPlan := &Plan{
					Type:      PlanJoin,
					Left:      leftPlan,
					Right:     rightPlan,
					RowCount:  o.cardinality.EstimateJoinSize(leftPlan.Table, rightPlan.Table, join.On),
					Predicate: join.On,
				}
				joinPlan.Cost = o.costModel.CostPlan(joinPlan)
				if joinPlan.Cost < bestCost {
					bestCost = joinPlan.Cost
					bestPlan = joinPlan
				}
			}
		}
	}

	return bestPlan
}

// Helper: generate subsets of a given size.
func (o *Optimizer) generateSubsets(tables []string, size int) [][]string {
	var result [][]string
	n := len(tables)
	if size > n {
		return result
	}

	// Use bitmask to generate subsets
	for mask := 0; mask < (1 << n); mask++ {
		count := 0
		for i := 0; i < n; i++ {
			if mask&(1<<i) != 0 {
				count++
			}
		}
		if count != size {
			continue
		}

		subset := make([]string, 0, size)
		for i := 0; i < n; i++ {
			if mask&(1<<i) != 0 {
				subset = append(subset, tables[i])
			}
		}
		result = append(result, subset)
	}
	return result
}

// subsetKey creates a unique key for a subset of tables.
func (o *Optimizer) subsetKey(tables []string) string {
	// Sort for consistent keys
	sorted := make([]string, len(tables))
	copy(sorted, tables)
	sort.Strings(sorted)
	key := ""
	for _, t := range sorted {
		key += t + ","
	}
	return key
}

// buildLeftDeepPlan creates a left-deep plan in original order (fallback).
func (o *Optimizer) buildLeftDeepPlan(tables []string, joins []core.JoinClause) *Plan {
	if len(tables) == 0 {
		return nil
	}

	// Start with first table
	plan := o.bestAccessPath(tables[0])

	// Join remaining tables left-deep
	for i := 1; i < len(tables); i++ {
		rightPlan := o.bestAccessPath(tables[i])

		// Find join predicate for this table
		var predicate core.Expr
		for _, join := range joins {
			if join.Table == tables[i] {
				predicate = join.On
				break
			}
		}

		joinPlan := &Plan{
			Type:      PlanJoin,
			Left:      plan,
			Right:     rightPlan,
			RowCount:  o.cardinality.EstimateJoinSize(tables[0], tables[i], predicate),
			Predicate: predicate,
		}
		joinPlan.Cost = o.costModel.CostPlan(joinPlan)
		plan = joinPlan
	}

	return plan
}
