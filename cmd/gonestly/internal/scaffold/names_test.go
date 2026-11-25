package scaffold

import "testing"

func TestNames(t *testing.T) {
	tests := map[string]Names{
		"products":    {Package: "products", Kebab: "products", Snake: "products", Model: "Product", Words: "products", ModelWords: "product"},
		"OrderItems":  {Package: "orderitems", Kebab: "order-items", Snake: "order_items", Model: "OrderItem", Words: "order items", ModelWords: "order item"},
		"categories":  {Package: "categories", Kebab: "categories", Snake: "categories", Model: "Category", Words: "categories", ModelWords: "category"},
		"api_keys":    {Package: "apikeys", Kebab: "api-keys", Snake: "api_keys", Model: "APIKey", Words: "api keys", ModelWords: "api key"},
		"boxes":       {Package: "boxes", Kebab: "boxes", Snake: "boxes", Model: "Box", Words: "boxes", ModelWords: "box"},
		"status":      {Package: "status", Kebab: "status", Snake: "status", Model: "Status", Words: "status", ModelWords: "status"},
		"HTTPClients": {Package: "httpclients", Kebab: "http-clients", Snake: "http_clients", Model: "HTTPClient", Words: "http clients", ModelWords: "http client"},
	}
	for in, want := range tests {
		got, err := NewNames(in)
		if err != nil || got != want {
			t.Errorf("NewNames(%q) = %+v, %v\nwant %+v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "--", "9lives", "no/slash", "café!"} {
		if _, err := NewNames(bad); err == nil {
			t.Errorf("NewNames(%q) succeeded", bad)
		}
	}
}
