package s3

import "testing"

func TestConfigValidate(t *testing.T) {
	valid := Config{
		LogicalStore:    "chill-evidence-store",
		Bucket:          "chill-profiling-evidence",
		Endpoint:        "http://chill-evidence-store.chill-system.svc.cluster.local:9000",
		Region:          "us-east-1",
		AccessKeyID:     "access-key",
		SecretAccessKey: "secret-key",
	}
	if err := valid.validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	tests := map[string]func(*Config){
		"missing store":      func(config *Config) { config.LogicalStore = "" },
		"missing bucket":     func(config *Config) { config.Bucket = "" },
		"store whitespace":   func(config *Config) { config.LogicalStore = " " + config.LogicalStore },
		"bucket whitespace":  func(config *Config) { config.Bucket += " " },
		"missing endpoint":   func(config *Config) { config.Endpoint = "" },
		"relative endpoint":  func(config *Config) { config.Endpoint = "chill-evidence-store:9000" },
		"endpoint query":     func(config *Config) { config.Endpoint += "?debug=true" },
		"missing region":     func(config *Config) { config.Region = "" },
		"missing access key": func(config *Config) { config.AccessKeyID = "" },
		"missing secret":     func(config *Config) { config.SecretAccessKey = "" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			config := valid
			mutate(&config)
			if err := config.validate(); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}

func TestNew(t *testing.T) {
	store, err := New(Config{
		LogicalStore:    "chill-evidence-store",
		Bucket:          "chill-profiling-evidence",
		Endpoint:        "http://chill-evidence-store.chill-system.svc.cluster.local:9000",
		Region:          "us-east-1",
		AccessKeyID:     "access-key",
		SecretAccessKey: "secret-key",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if store == nil || store.client == nil || store.logicalStore != "chill-evidence-store" ||
		store.bucket != "chill-profiling-evidence" {
		t.Fatal("New() returned an unusable Store")
	}
}
