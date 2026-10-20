package kube

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"
)

// M4 P4: 64 closed-loop inference callers share ChillServer's Kubernetes
// client (--kube-api-qps 50, --kube-api-burst 100) with scheduler capture.
func TestAuthorizerLeavesKubernetesClientBudgetUnderInferenceLoad(t *testing.T) {
	var reviews atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := `{"apiVersion":"v1","kind":"NodeList","items":[]}`
		switch r.URL.Path {
		case "/apis/authentication.k8s.io/v1/tokenreviews":
			reviews.Add(1)
			body = `{"apiVersion":"authentication.k8s.io/v1","kind":"TokenReview","status":{"authenticated":true,"user":{"username":"client","uid":"client-uid"}}}`
		case "/apis/authorization.k8s.io/v1/subjectaccessreviews":
			reviews.Add(1)
			body = `{"apiVersion":"authorization.k8s.io/v1","kind":"SubjectAccessReview","status":{"allowed":true}}`
		}
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()
	kube, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL, QPS: 50, Burst: 100})
	if err != nil {
		t.Fatal(err)
	}
	authorizer := &Authorizer{Client: kube}
	const window = 3 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), window)
	defer cancel()
	var admitted atomic.Int64
	var wg sync.WaitGroup
	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				if _, err := authorizer.Authorize(ctx, "token", "POST", "/api/v1/workloads/tight/infer"); err == nil {
					admitted.Add(1)
				}
			}
		}()
	}
	time.Sleep(time.Second)
	started := time.Now()
	if _, err := kube.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{}); err != nil {
		t.Fatal(err)
	}
	listWait := time.Since(started)
	wg.Wait()
	rate := float64(admitted.Load()) / window.Seconds()
	t.Logf("admitted %.1f req/s, %d reviews, capture Node list %v", rate, reviews.Load(), listWait)
	if rate < 200 || listWait > 250*time.Millisecond {
		t.Fatalf("inference authorization spends the shared client budget: %.1f req/s, Node list waited %v", rate, listWait)
	}
}

func TestAuthorizerReusesOnlyAllowedReviews(t *testing.T) {
	client := fake.NewSimpleClientset()
	reviews := 0
	client.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		reviews++
		return true, &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{
			Authenticated: true, User: authenticationv1.UserInfo{Username: "client", UID: "client-uid"},
		}}, nil
	})
	client.PrependReactor("create", "subjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
		review := action.(ktesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview)
		return true, &authorizationv1.SubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{
			Allowed: review.Spec.NonResourceAttributes.Path == "/chill/api/v1/workloads/tight/infer",
		}}, nil
	})
	authorizer := &Authorizer{Client: client}
	for _, step := range []struct {
		token, path string
		allowed     bool
		reviews     int
	}{
		{"a", "/api/v1/workloads/tight/infer", true, 1},
		{"a", "/api/v1/workloads/tight/infer", true, 1},
		{"b", "/api/v1/workloads/tight/infer", true, 2},
		{"a", "/api/v1/models", false, 3},
		{"a", "/api/v1/models", false, 4},
	} {
		_, err := authorizer.Authorize(context.Background(), step.token, "POST", step.path)
		if (err == nil) != step.allowed || reviews != step.reviews {
			t.Fatalf("%+v: err=%v reviews=%d", step, err, reviews)
		}
	}
}
