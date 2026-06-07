package dns

// apply.go — declarative diff for DNS-as-Code (M6). diffRecords compares
// a desired record set against the current one and produces a plan of
// create/update/delete actions. Pure (no DB/provider) so it's unit-tested
// directly; the handler (apply_handlers.go) executes the plan via the
// provider then refreshes the cache.
//
// Model (octoDNS-style): records are grouped by (name, type). Within a
// declared group the desired *value set* is authoritative — extra values
// in that group are deleted, missing ones created, matching ones updated
// when ttl/priority/proxied drift. Groups NOT mentioned in the desired
// set are left alone unless prune=true, and SOA/NS are never auto-deleted
// (provider-managed).

import "strings"

type recordPlan struct {
	Create []Record `json:"create"`
	Update []Record `json:"update"` // carries the matched RemoteID + new fields
	Delete []Record `json:"delete"` // carries RemoteID
}

func (p recordPlan) empty() bool {
	return len(p.Create) == 0 && len(p.Update) == 0 && len(p.Delete) == 0
}

func recGroupKey(r Record) string {
	return normalizeHost(r.Name) + "|" + strings.ToUpper(strings.TrimSpace(r.Type))
}

// isProviderManagedType reports record types we never auto-delete on prune.
func isProviderManagedType(t string) bool {
	switch strings.ToUpper(strings.TrimSpace(t)) {
	case "SOA", "NS":
		return true
	}
	return false
}

func priorityEqual(a, b *int) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func diffRecords(current, desired []Record, prune bool) recordPlan {
	var plan recordPlan

	curByGroup := map[string][]Record{}
	for _, c := range current {
		k := recGroupKey(c)
		curByGroup[k] = append(curByGroup[k], c)
	}

	// Preserve desired order for stable create/update output.
	desByGroup := map[string][]Record{}
	var order []string
	for _, d := range desired {
		k := recGroupKey(d)
		if _, ok := desByGroup[k]; !ok {
			order = append(order, k)
		}
		desByGroup[k] = append(desByGroup[k], d)
	}

	seen := map[string]bool{}
	for _, gk := range order {
		seen[gk] = true
		desList := desByGroup[gk]
		curByContent := map[string]Record{}
		for _, c := range curByGroup[gk] {
			curByContent[c.Content] = c
		}
		desContents := map[string]bool{}
		for _, d := range desList {
			desContents[d.Content] = true
			if cur, ok := curByContent[d.Content]; ok {
				if cur.TTL != d.TTL || !priorityEqual(cur.Priority, d.Priority) || cur.Proxied != d.Proxied {
					u := d
					u.RemoteID = cur.RemoteID
					plan.Update = append(plan.Update, u)
				}
				continue
			}
			plan.Create = append(plan.Create, d)
		}
		// Extra values in a declared group → delete.
		for _, c := range curByGroup[gk] {
			if !desContents[c.Content] {
				plan.Delete = append(plan.Delete, c)
			}
		}
	}

	if prune {
		for gk, curList := range curByGroup {
			if seen[gk] {
				continue
			}
			for _, c := range curList {
				if !isProviderManagedType(c.Type) {
					plan.Delete = append(plan.Delete, c)
				}
			}
		}
	}
	return plan
}
