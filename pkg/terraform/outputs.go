package terraform

import (
	"encoding/json"
	"fmt"
)

// TerraformOutput represents a single Terraform output value
type TerraformOutput struct {
	Sensitive bool        `json:"sensitive"`
	Type      interface{} `json:"type"`
	Value     interface{} `json:"value"`
}

// ParseOutputs parses Terraform JSON output into a string map
func ParseOutputs(jsonData []byte) (map[string]string, error) {
	if len(jsonData) == 0 {
		return map[string]string{}, nil
	}

	var outputs map[string]TerraformOutput
	if err := json.Unmarshal(jsonData, &outputs); err != nil {
		return nil, fmt.Errorf("failed to unmarshal terraform outputs: %w", err)
	}

	result := make(map[string]string)
	for key, output := range outputs {
		// Convert output value to string
		switch v := output.Value.(type) {
		case string:
			result[key] = v
		case float64:
			result[key] = fmt.Sprintf("%v", v)
		case bool:
			result[key] = fmt.Sprintf("%v", v)
		default:
			// For complex types, marshal to JSON string
			jsonBytes, err := json.Marshal(v)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal output %s: %w", key, err)
			}
			result[key] = string(jsonBytes)
		}
	}

	return result, nil
}
