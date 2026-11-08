package serving

// ResolvePathBudget selects the active budget for this exact Workload definition.
// Configured margins retain their historical pre-F double-reservation limitation;
// they are estimates, not observed confidence bounds.
func ResolvePathBudget(settings SchedulerSettings, budgets []PathBudget, w *Definition) PathBudget {
	identity := w.Identity()
	selected := PathBudget{WorkloadUID: w.UID, DefinitionID: identity}
	for _, budget := range budgets {
		if budget.WorkloadUID == w.UID && budget.DefinitionID == identity {
			if budget.Source == PathBudgetObserved && settings.UsesObservedPathBudgets() {
				return budget
			}
			selected = budget
			break
		}
	}
	selected.Source = PathBudgetConfigured
	selected.TotalMS, selected.PostMS = settings.CompletionMarginMS, settings.CompletionMarginMS
	selected.JudgmentPercentile = 100 - (100-w.SLO.Percentile)/2
	return selected
}

// replayPathMargins allocates TOTAL once in synthetic latency. The sender reserve
// is a modeling allocation, never an observed pre-F quantile. If POST exceeds
// TOTAL, native admission conservatively reserves POST without a negative sender.
func replayPathMargins(budget PathBudget) (senderMS, returnMS float64) {
	return max(0, budget.TotalMS-budget.PostMS), budget.TotalMS
}
