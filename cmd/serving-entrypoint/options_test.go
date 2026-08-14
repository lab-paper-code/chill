package main

import "testing"

func validOptions() options {
	return options{
		bindAddress:      ":8080",
		routesPath:       "/etc/chill/routes.yaml",
		servingNamespace: "chill-system",
		servingPort:      8000,
	}
}

func TestOptionsValidate(t *testing.T) {
	t.Parallel()
	if err := validOptions().validate(); err != nil {
		t.Fatalf("valid options rejected: %v", err)
	}
	for _, testCase := range []struct {
		name   string
		mutate func(*options)
	}{
		{name: "no bind address", mutate: func(o *options) { o.bindAddress = " " }},
		{name: "a relative routing table path", mutate: func(o *options) { o.routesPath = "routes.yaml" }},
		{name: "no routing table at all", mutate: func(o *options) { o.routesPath = "" }},
		{name: "no serving namespace", mutate: func(o *options) { o.servingNamespace = "" }},
		{name: "a serving namespace that is not a label", mutate: func(o *options) { o.servingNamespace = "Chill System" }},
		{name: "no serving port", mutate: func(o *options) { o.servingPort = 0 }},
		{name: "a serving port outside the range", mutate: func(o *options) { o.servingPort = 70000 }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			invalid := validOptions()
			testCase.mutate(&invalid)
			if err := invalid.validate(); err == nil {
				t.Fatalf("invalid options accepted: %#v", invalid)
			}
		})
	}
}
