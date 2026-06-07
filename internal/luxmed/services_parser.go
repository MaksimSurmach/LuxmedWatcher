package luxmed

import "github.com/maksimsurmach/luxmed-watcher/internal/domain"

func normalizeServices(payload any) []domain.Service {
	seen := make(map[int]bool)
	var services []domain.Service

	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case []any:
			for _, child := range typed {
				walk(child)
			}

		case map[string]any:
			children, hasChildren := typed["children"]
			if hasChildren && hasItems(children) {
				walk(children)
				return
			}

			id := intFromAny(firstValue(typed,
				"id",
				"serviceVariantId",
				"serviceId",
				"value",
				"key",
			))

			name := stringFromAny(firstValue(typed,
				"name",
				"serviceVariantName",
				"serviceName",
				"label",
				"text",
				"displayName",
				"title",
			))

			if id > 0 && name != "" && !seen[id] {
				seen[id] = true
				services = append(services, domain.Service{
					ID:   id,
					Name: name,
				})
			}

			for _, key := range []string{
				"children",
				"items",
				"data",
				"result",
				"results",
				"serviceVariants",
				"serviceVariantGroups",
				"groups",
				"values",
			} {
				child, ok := typed[key]
				if !ok {
					continue
				}
				walk(child)
			}
		}
	}

	walk(payload)
	return services
}

func hasItems(value any) bool {
	switch typed := value.(type) {
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return false
	}
}
