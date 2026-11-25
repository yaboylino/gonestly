package validator_test

import "encoding/json"

func jsonBody(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}
