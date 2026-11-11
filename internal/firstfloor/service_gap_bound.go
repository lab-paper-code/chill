package firstfloor

// MaximumQuantileNanos returns the exact largest conditional inverse-CDF value
// over every integer gap in [lower, upper], and a gap attaining it. No sampling
// or monotonicity assumption is used. Support covers every possible mixture;
// an unknown mixture makes the bound unknown. Value ties choose the smaller gap.
func (d *ServiceDistribution) MaximumQuantileNanos(lower, upper int64, percentile float64) (int64, int64, ConditionalSupport, bool) {
	return d.maximumQuantileNanos(lower, upper, 0, percentile, false)
}

// MaximumResidualQuantileNanos additionally bounds survival-conditioned residuals
// when an initial replay batch's actual preceding gap is known only as a range.
func (d *ServiceDistribution) MaximumResidualQuantileNanos(lower, upper, elapsed int64, percentile float64) (int64, int64, ConditionalSupport, bool) {
	return d.maximumQuantileNanos(lower, upper, elapsed, percentile, true)
}

func (d *ServiceDistribution) maximumQuantileNanos(lower, upper, elapsed int64, percentile float64, residual bool) (int64, int64, ConditionalSupport, bool) {
	if lower < 0 || upper < lower {
		return 0, 0, ConditionalSupport{}, false
	}
	at := func(gap int64) (int64, ConditionalSupport, bool) {
		if residual {
			return d.ResidualQuantileNanos(&gap, elapsed, percentile)
		}
		return d.QuantileNanos(&gap, percentile)
	}
	value, support, known := at(lower)
	if !known {
		return 0, 0, support, false
	}
	maximizing := lower
	query := func(gap int64) bool {
		if gap < lower || gap > upper {
			return true
		}
		next, evidence, ok := at(gap)
		support.DistanceNanos = max(support.DistanceNanos, evidence.DistanceNanos)
		support.TrialCount = min(support.TrialCount, evidence.TrialCount)
		support.OutsideSupport = support.OutsideSupport || evidence.OutsideSupport
		if !ok {
			return false
		}
		if next > value || next == value && gap < maximizing {
			value, maximizing = next, gap
		}
		return true
	}
	boundary := func(left, right int64) bool {
		midpoint := left + (right-left)/2
		return query(midpoint) && query(midpoint+1)
	}
	if !query(upper) {
		return 0, 0, support, false
	}
	for i := range d.trials {
		groups := d.trials[i].groups
		// The ordinary nearest mixture changes at each adjacent midpoint. Integer
		// ties are separate mixtures, so query floor(midpoint) and its successor.
		for j := 1; j < len(groups); j++ {
			if !boundary(groups[j-1].gap, groups[j].gap) {
				return 0, 0, support, false
			}
		}
		if residual {
			// On an exhausted nearest mixture, retry uses nearest surviving groups.
			// Their adjacent boundaries can differ from those in the full evidence.
			previous := -1
			for j := range groups {
				rows := groups[j].samples
				if rows[len(rows)-1].ServiceDurationNanos <= elapsed {
					continue
				}
				if previous >= 0 && !boundary(groups[previous].gap, groups[j].gap) {
					return 0, 0, support, false
				}
				previous = j
			}
		}
	}
	return value, maximizing, support, true
}
