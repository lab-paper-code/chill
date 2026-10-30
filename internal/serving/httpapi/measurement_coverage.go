package httpapi

import (
 "errors"
 "net/http"
 "net/url"
 "strconv"
 "strings"

 api "github.com/lab-paper-code/chill/api/v1alpha1"
 "github.com/lab-paper-code/chill/internal/serving"
 servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
 authorizationv1 "k8s.io/api/authorization/v1"
 "k8s.io/apimachinery/pkg/util/validation"
)

// MeasurementCoverageQuery is transport-owned; the composition root connects
// this HTTP contract to the shared assessment projection.
type MeasurementCoverageQuery struct { Batches []int; Node string }

func parseMeasurementCoverageQuery(values url.Values)(MeasurementCoverageQuery,error){
 query:=MeasurementCoverageQuery{Batches:[]int{}}
 invalid:=&serving.Error{Code:"InvalidCoverageQuery",Message:"batches는 중복 없는 오름차순 양의 정수, node는 정확한 이름이어야 합니다"}
 for key,items:=range values {
  if (key!="batches"&&key!="node")||len(items)!=1||items[0]==""{return query,invalid}
  if key=="node" {if len(validation.IsDNS1123Subdomain(items[0]))!=0{return query,invalid};query.Node=items[0];continue}
  previous:=0
  for _,raw:=range strings.Split(items[0],",") {batch,err:=strconv.Atoi(raw);if err!=nil||batch<=previous||strconv.Itoa(batch)!=raw||batch>2147483647{return query,invalid};query.Batches=append(query.Batches,batch);previous=batch}
 }
 return query,nil
}

func(h *Handler)measurementCoverage(w http.ResponseWriter,r *http.Request){
 // Both URL grant and native namespace grants precede every cache access.
 if err:=h.authorize(r);err!=nil{sendError(w,err);return}
 namespace:=r.PathValue("namespace")
 if len(validation.IsDNS1123Label(namespace))!=0{sendError(w,&serving.Error{Code:"InvalidCoverageQuery",Message:"유효한 namespace가 필요합니다"});return}
 principal:=servingkube.PrincipalFromContext(r.Context())
 for _,resource:=range []string{"batchenergyoptimizations","profilingruns"}{
  if err:=h.authorizer.AuthorizeResource(r.Context(),principal,authorizationv1.ResourceAttributes{Namespace:namespace,Verb:"list",Group:api.GroupVersion.Group,Version:api.GroupVersion.Version,Resource:resource});err!=nil{sendError(w,err);return}
 }
 values,err:=url.ParseQuery(r.URL.RawQuery)
 if err!=nil{sendError(w,&serving.Error{Code:"InvalidCoverageQuery",Message:"유효한 query가 필요합니다"});return}
 query,err:=parseMeasurementCoverageQuery(values)
 if err!=nil{sendError(w,err);return}
 if h.config.MeasurementCoverage==nil{sendError(w,measurementUnavailable());return}
 value,err:=h.config.MeasurementCoverage(r.Context(),namespace,query)
 if err!=nil{var domain *serving.Error;if errors.As(err,&domain)&&domain.Code=="CoverageWarming"{w.Header().Set("Retry-After","2")};sendError(w,err);return}
 sendJSON(w,http.StatusOK,value)
}
