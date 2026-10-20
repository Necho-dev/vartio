package agent

// clampMem keeps compressed inside used and cached inside the reclaimable
// remainder, so the three readings never add up past total.
func clampMem(total, available, compressed, cached uint64) (used, comp, cache uint64) {
	if available > total {
		available = total
	}
	used = total - available
	comp = compressed
	if comp > used {
		comp = used
	}
	cache = cached
	if cache > available {
		cache = available
	}
	return used, comp, cache
}
