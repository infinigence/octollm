package limiter

type contextKey string

const (
	// ContextKeyPriority is the key used to store priority in context
	ContextKeyPriority contextKey = "priority_key"
)

const (
	// ContextValuePrefixPriority is the prefix for priority values, used for serialization and deserialization
	ContextValuePrefixPriority string = "priority_value_prefix"
)
