package s3

import "testing"

func TestConfigValidate(t *testing.T) {
	valid := Config{
		Endpoint:        "http://chill-evidence-store.chill-system.svc.cluster.local:9000",
		Region:          "us-east-1",
		AccessKeyID:     "access-key",
		SecretAccessKey: "secret-key",
	}
	if err := valid.validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	tests := map[string]Config{
		"missing endpoint":  {Region: valid.Region, AccessKeyID: valid.AccessKeyID, SecretAccessKey: valid.SecretAccessKey},
		"relative endpoint": {Endpoint: "chill-evidence-store:9000", Region: valid.Region, AccessKeyID: valid.AccessKeyID, SecretAccessKey: valid.SecretAccessKey},
		"endpoint query":    {Endpoint: valid.Endpoint + "?debug=true", Region: valid.Region, AccessKeyID: valid.AccessKeyID, SecretAccessKey: valid.SecretAccessKey},
		"missing region":    {Endpoint: valid.Endpoint, AccessKeyID: valid.AccessKeyID, SecretAccessKey: valid.SecretAccessKey},
		"missing access key": {
			Endpoint: valid.Endpoint, Region: valid.Region, SecretAccessKey: valid.SecretAccessKey,
		},
		"missing secret": {Endpoint: valid.Endpoint, Region: valid.Region, AccessKeyID: valid.AccessKeyID},
	}
	for name, config := range tests {
		t.Run(name, func(t *testing.T) {
			if err := config.validate(); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}

func TestNew(t *testing.T) {
	store, err := New(Config{
		Endpoint:        "http://chill-evidence-store.chill-system.svc.cluster.local:9000",
		Region:          "us-east-1",
		AccessKeyID:     "access-key",
		SecretAccessKey: "secret-key",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if store == nil || store.client == nil {
		t.Fatal("New() returned an unusable Store")
	}
}
