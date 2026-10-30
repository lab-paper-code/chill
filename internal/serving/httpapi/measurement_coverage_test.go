package httpapi

import (
 "context"
 "encoding/json"
 "net/http"
 "net/http/httptest"
 "net/url"
 "testing"

 "github.com/lab-paper-code/chill/internal/serving"
 servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
 authentication "k8s.io/api/authentication/v1"
 authorization "k8s.io/api/authorization/v1"
 "k8s.io/apimachinery/pkg/runtime"
 "k8s.io/client-go/kubernetes/fake"
 ktesting "k8s.io/client-go/testing"
)

func TestMeasurementCoverageStrictQuery(t *testing.T){
 for _,raw:=range []string{"batches=2,1","batches=1,1","batches=0","batches=01","batches=","node=","node=a&node=b","model=a","batches=2147483648"}{values,_:=url.ParseQuery(raw);if _,err:=parseMeasurementCoverageQuery(values);err==nil{t.Errorf("accepted %s",raw)}}
 values,_:=url.ParseQuery("batches=1,2,4&node=node-a");query,err:=parseMeasurementCoverageQuery(values);if err!=nil||len(query.Batches)!=3||query.Node!="node-a"{t.Fatalf("%+v %v",query,err)}
}

func TestMeasurementCoverageGrantsBeforeCache(t *testing.T){
 for _,deny:=range []string{"url","batchenergyoptimizations","profilingruns",""}{t.Run(deny,func(t *testing.T){
  kube:=fake.NewSimpleClientset()
  kube.PrependReactor("create","tokenreviews",func(ktesting.Action)(bool,runtime.Object,error){return true,&authentication.TokenReview{Status:authentication.TokenReviewStatus{Authenticated:true,User:authentication.UserInfo{Username:"caller"}}},nil})
  resources:=[]string{}
  kube.PrependReactor("create","subjectaccessreviews",func(action ktesting.Action)(bool,runtime.Object,error){review:=action.(ktesting.CreateAction).GetObject().(*authorization.SubjectAccessReview);allowed:=true
   if resource:=review.Spec.ResourceAttributes;resource!=nil{resources=append(resources,resource.Resource);if resource.Namespace!="allowed"||resource.Verb!="list"||resource.Group!="edge.dacs.io"||resource.Version!="v1alpha1"{t.Fatalf("wrong namespace grant: %+v",resource)};allowed=deny!=resource.Resource}else{if review.Spec.NonResourceAttributes.Path!="/chill/api/v1/namespaces/allowed/measurement-coverage"||review.Spec.NonResourceAttributes.Verb!="get"{t.Fatalf("wrong URL grant: %+v",review.Spec.NonResourceAttributes)};allowed=deny!="url"}
   return true,&authorization.SubjectAccessReview{Status:authorization.SubjectAccessReviewStatus{Allowed:allowed}},nil})
  calls:=0
  h:=&Handler{authorizer:&servingkube.Authorizer{Client:kube},config:HandlerConfig{MeasurementCoverage:func(context.Context,string,MeasurementCoverageQuery)(json.RawMessage,error){calls++;return nil,&serving.Error{Code:"CoverageWarming",Message:"warming"}}}}
  request:=httptest.NewRequest(http.MethodGet,"/api/v1/namespaces/allowed/measurement-coverage",nil);request.Header.Set("Authorization","Bearer caller-token");request.SetPathValue("namespace","allowed")
  response:=httptest.NewRecorder();h.measurementCoverage(response,request)
  if deny!=""{if response.Code!=http.StatusForbidden||calls!=0{t.Fatalf("cache accessed after denied grant: %d %d",response.Code,calls)}}else{if response.Code!=http.StatusServiceUnavailable||calls!=1||response.Header().Get("Retry-After")!="2"||response.Header().Get("Cache-Control")!="no-store"||len(resources)!=2{t.Fatalf("warming contract: %d %v %v",response.Code,response.Header(),resources)}}
 })}
}
