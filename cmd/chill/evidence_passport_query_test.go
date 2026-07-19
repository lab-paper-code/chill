package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestServiceProxyEvidencePassportQueryUsesKubernetesServiceProxy(t *testing.T) {
	t.Parallel()
	passport := testEvidencePassport()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		want := "/api/v1/namespaces/chill-system/services/chill-console:http/proxy/api/v1/namespaces/chill-system/profiling-runs/cpu-run/evidence-passport"
		if request.Method != http.MethodGet || request.URL.Path != want {
			t.Fatalf("request = %s %s, want GET %s", request.Method, request.URL.Path, want)
		}
		writer.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(writer).Encode(passport); err != nil {
			t.Fatal(err)
		}
	}))
	defer server.Close()
	clientset, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	query := &serviceProxyEvidencePassportQuery{
		restClient: clientset.CoreV1().RESTClient(), namespace: "chill-system",
		service: "chill-console", run: "cpu-run",
	}
	got, err := query.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Run.UID != passport.Run.UID || got.Measurement.Runtime.Latency.P99 != passport.Measurement.Runtime.Latency.P99 {
		t.Fatalf("passport = %#v", got)
	}
}

func TestServiceProxyEvidencePassportQueryRejectsUnexpectedIdentityAndOversize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		payload func() []byte
		match   string
	}{
		{
			name: "identity mismatch",
			payload: func() []byte {
				passport := testEvidencePassport()
				passport.Run.Name = "other"
				payload, _ := json.Marshal(passport)
				return payload
			},
			match: "unexpected Evidence Passport identity",
		},
		{
			name: "oversize",
			payload: func() []byte {
				return []byte(strings.Repeat("x", int(maximumEvidencePassportBytes)+1))
			},
			match: "size limit",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write(test.payload())
			}))
			defer server.Close()
			clientset, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			query := &serviceProxyEvidencePassportQuery{
				restClient: clientset.CoreV1().RESTClient(), namespace: "chill-system",
				service: "chill-console", run: "cpu-run",
			}
			_, err = query.Read(context.Background())
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("Read() error = %v, want %q", err, test.match)
			}
		})
	}
}

func TestEvidencePassportQueryRejectsWrongAPIVersion(t *testing.T) {
	t.Parallel()
	passport := testEvidencePassport()
	passport.APIVersion = "future"
	payload, err := json.Marshal(passport)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(payload)
	}))
	defer server.Close()
	clientset, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	query := &serviceProxyEvidencePassportQuery{
		restClient: clientset.CoreV1().RESTClient(), namespace: "chill-system",
		service: "chill-console", run: "cpu-run",
	}
	_, err = query.Read(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unexpected Evidence Passport identity") {
		t.Fatalf("Read() error = %v", err)
	}
}
