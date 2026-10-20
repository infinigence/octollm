package limiter

import (
	"context"
	"fmt"
)

func priorityFromNamespace(ctx context.Context, nameSpace string) (int, bool) {
	key := contextKey(fmt.Sprintf("%s:%s", nameSpace, ContextKeyPriority))
	priorityStr, ok := ctx.Value(key).(string)
	if !ok {
		return 0, false
	}
	var priority int
	_, err := fmt.Sscanf(priorityStr, ContextValuePrefixPriority+"%d", &priority)
	if err != nil {
		return 0, false
	}
	return priority, true
}

func setPriorityToContext(ctx context.Context, nameSpace string, priority int) context.Context {
	priorityStr := fmt.Sprintf("%s%d", ContextValuePrefixPriority, priority)
	key := contextKey(fmt.Sprintf("%s:%s", nameSpace, ContextKeyPriority))
	return context.WithValue(ctx, key, priorityStr)
}

// qosIndex maps a request priority onto the high-to-low QoS arrays.
// Missing or negative priority uses the lowest band; priority >= n-1 uses the highest.
func qosIndex(priority, n int) int {
	if n <= 0 {
		return 0
	}
	if priority < 0 {
		return n - 1
	}
	if priority >= n-1 {
		return 0
	}
	return n - 1 - priority
}
