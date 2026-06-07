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
			if children, ok := typed["children"]; ok {
				walk(children)
				return
			}
			id := intFromAny(firstValue(typed, "id", "serviceVariantId", "serviceId"))
			name := stringFromAny(firstValue(typed, "name", "serviceVariantName", "serviceName"))
			if id > 0 && name != "" && !seen[id] {
				seen[id] = true
				services = append(services, domain.Service{ID: id, Name: name})
			}
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(payload)
	return services
}
