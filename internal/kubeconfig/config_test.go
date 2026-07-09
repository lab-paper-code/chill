package kubeconfig

import "testing"

func TestBuildConfigUsesExplicitAPIServer(t *testing.T) {
	config, err := BuildConfig(Options{
		APIServer: " https://kubernetes.default.svc:443 ",
	})
	if err != nil {
		t.Fatalf("BuildConfig() error = %v", err)
	}
	if config.Host != "https://kubernetes.default.svc:443" {
		t.Fatalf("Host = %q, want explicit API server", config.Host)
	}
	if config.BearerTokenFile != DefaultServiceAccountTokenFile {
		t.Fatalf("BearerTokenFile = %q, want default service account token", config.BearerTokenFile)
	}
	if config.TLSClientConfig.CAFile != DefaultServiceAccountCAFile {
		t.Fatalf("CAFile = %q, want default service account CA", config.TLSClientConfig.CAFile)
	}
}

func TestBuildConfigPreservesExplicitTokenAndCAFiles(t *testing.T) {
	config, err := BuildConfig(Options{
		APIServer: "https://edge-meta-server:10550",
		TokenFile: "/token",
		CAFile:    "/ca.crt",
	})
	if err != nil {
		t.Fatalf("BuildConfig() error = %v", err)
	}
	if config.BearerTokenFile != "/token" || config.TLSClientConfig.CAFile != "/ca.crt" {
		t.Fatalf("config token/CA files = %q/%q, want explicit files", config.BearerTokenFile, config.TLSClientConfig.CAFile)
	}
}
