package serving

import "context"

type capacityLoad struct {
	ctx  context.Context
	keys []string
}

func (t *CapacityTable) startLoadLocked() {
	request := &capacityLoad{}
	for _, uid := range t.order {
		state := t.groups[uid]
		for _, g := range []*capacityGroup{state.active, state.building, state.latest} {
			if g == nil {
				continue
			}
			select {
			case <-g.ctx.Done():
				continue
			default:
			}
			for _, item := range g.items {
				if _, built := t.rows[item.key]; built || t.asked[item.key] != nil {
					continue
				}
				request.ctx = g.ctx
				request.keys = append(request.keys, item.key)
				t.asked[item.key] = request
			}
		}
	}
	if len(request.keys) == 0 {
		return
	}
	t.loading = request
	go func() {
		rows, err := t.load(request.ctx, request.keys)
		t.mu.Lock()
		if err == nil {
			for key, row := range rows {
				if _, built := t.rows[key]; !built && t.asked[key] == request {
					row.Previous = true
					t.previous[key] = row
				}
			}
		}
		request.ctx, request.keys = nil, nil
		t.loading = nil
		t.startLocked()
		t.idle.Broadcast()
		t.mu.Unlock()
	}()
}

func (t *CapacityTable) pruneLocked() {
	keys := map[string]bool{}
	for _, state := range t.groups {
		for _, g := range []*capacityGroup{state.active, state.building, state.latest} {
			if g != nil {
				for _, item := range g.items {
					keys[item.key] = true
				}
			}
		}
	}
	for key := range t.rows {
		if !keys[key] {
			delete(t.rows, key)
		}
	}
	for key := range t.previous {
		if !keys[key] {
			delete(t.previous, key)
		}
	}
	for key := range t.asked {
		if !keys[key] {
			delete(t.asked, key)
		}
	}
}
