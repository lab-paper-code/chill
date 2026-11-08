package serving

type runtimePathMargins struct {
	totalMS, postMS float64
}

// SetPathBudgetPolicy applies accepted policy to new calls atomically. Disabling
// observed budgets removes per-definition overrides; accepted calls stay frozen.
func (r *Runtime) SetPathBudgetPolicy(settings SchedulerSettings) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.completionMargin = settings.CompletionMarginMS
	if !settings.UsesObservedPathBudgets() {
		r.pathBudgets = nil
	}
}

// SetPathBudgets publishes active per-definition margins for new calls.
// Copy only numerical defaults outside the owner lock, never sample diagnostics.
func (r *Runtime) SetPathBudgets(budgets []PathBudget) {
	margins := make(map[string]runtimePathMargins, len(budgets))
	for _, budget := range budgets {
		margins[budget.DefinitionID] = runtimePathMargins{totalMS: budget.TotalMS, postMS: budget.PostMS}
	}
	r.mu.Lock()
	r.pathBudgets = margins
	r.mu.Unlock()
}

func (r *Runtime) pathMarginsLocked(identity string) runtimePathMargins {
	if margins, ok := r.pathBudgets[identity]; ok {
		return margins
	}
	return runtimePathMargins{totalMS: r.completionMargin, postMS: r.completionMargin}
}
