package agent

import (
	"encoding/json"
	"fmt"
)

// canonicalSignature normalizes an action and its input for repeat detection.
// It removes whitespace and object key-order differences by decoding to any and
// re-encoding the pair.
func canonicalSignature(action string, input json.RawMessage) (string, error) {
	var value any
	if err := json.Unmarshal(input, &value); err != nil {
		return "", fmt.Errorf("decode action_input: %w", err)
	}

	normalized, err := json.Marshal([]any{action, value})
	if err != nil {
		return "", fmt.Errorf("encode action signature: %w", err)
	}
	return string(normalized), nil
}
