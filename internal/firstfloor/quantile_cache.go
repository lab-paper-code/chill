package firstfloor

import "math"

// QuantileCache belongs to one dispatch, never to a shared distribution. Reset
// releases distribution identities while retaining storage for the next dispatch.
// Values, support and failed queries all retain the distribution's exact result.
type QuantileCache struct {
	entries map[quantileQuery]quantileResult
}
type quantileQuery struct {
	distribution      *ServiceDistribution
	gap, elapsed      int64
	percentile        uint64
	startup, residual bool
}
type quantileResult struct {
	value   int64
	support ConditionalSupport
	known   bool
}

func (c *QuantileCache) Reset() { clear(c.entries) }

func (c *QuantileCache) QuantileNanos(d *ServiceDistribution, gap *int64, percentile float64) (int64, ConditionalSupport, bool) {
	return c.query(d, gap, 0, percentile, false)
}
func (c *QuantileCache) ResidualQuantileNanos(d *ServiceDistribution, gap *int64, elapsed int64, percentile float64) (int64, ConditionalSupport, bool) {
	return c.query(d, gap, elapsed, percentile, true)
}
func (c *QuantileCache) query(d *ServiceDistribution, gap *int64, elapsed int64, percentile float64, residual bool) (int64, ConditionalSupport, bool) {
	key := quantileQuery{distribution: d, elapsed: elapsed, percentile: math.Float64bits(percentile), startup: gap == nil, residual: residual}
	if gap != nil {
		key.gap = *gap
	}
	if c != nil {
		if result, found := c.entries[key]; found {
			return result.value, result.support, result.known
		}
	}
	var result quantileResult
	if residual {
		result.value, result.support, result.known = d.ResidualQuantileNanos(gap, elapsed, percentile)
	} else {
		result.value, result.support, result.known = d.QuantileNanos(gap, percentile)
	}
	if c != nil {
		if c.entries == nil {
			c.entries = make(map[quantileQuery]quantileResult)
		}
		c.entries[key] = result
	}
	return result.value, result.support, result.known
}
