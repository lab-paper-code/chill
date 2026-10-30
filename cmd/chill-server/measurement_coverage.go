package main

import (
 "context"
 "encoding/json"
 "errors"
 "io"
 "net/http"
 "slices"
 "sync"
 "time"

 api "github.com/lab-paper-code/chill/api/v1alpha1"
 "github.com/lab-paper-code/chill/internal/operatorconsole"
 "github.com/lab-paper-code/chill/internal/serving"
 "github.com/lab-paper-code/chill/internal/serving/httpapi"
 "sigs.k8s.io/controller-runtime/pkg/client"
)

type measurementCoverageCache struct { mu sync.Mutex; namespaces map[string]*measurementCoverageEntry }
type measurementCoverageEntry struct { base *measurementCoverageBase; refreshing bool; attempted bool; err error; lastAttempt time.Time }
type measurementCoverageBase struct {
 inventory operatorconsole.InventoryView
 compatibility operatorconsole.ModelCompatibilityMatrixView
 requests api.BatchEnergyOptimizationList
 runs api.ProfilingRunList
 catalog httpapi.ProfilesView
 adopted map[string]bool
 checked map[string]time.Time
 failures map[string]error
}

func (o *measurementService) coverage(ctx context.Context,namespace string,query httpapi.MeasurementCoverageQuery)(json.RawMessage,error){
 if err:=o.namespace(namespace);err!=nil{return nil,err}
 cache:=&o.coverageCache
 cache.mu.Lock()
 if cache.namespaces==nil{cache.namespaces=map[string]*measurementCoverageEntry{}}
 entry:=cache.namespaces[namespace];if entry==nil{entry=&measurementCoverageEntry{};cache.namespaces[namespace]=entry}
 base:=entry.base
 fresh:=base!=nil&&time.Since(base.oldest())<=30*time.Second&&len(base.failures)==0
 refreshDue:=base==nil||time.Since(entry.lastAttempt)>=30*time.Second
 if !fresh&&!entry.refreshing&&refreshDue {
  entry.refreshing=true;entry.lastAttempt=time.Now()
  go func(previous *measurementCoverageBase){
   refreshCtx,cancel:=context.WithTimeout(context.Background(),o.opts.requestTimeout);defer cancel()
   updated,err:=o.readCoverage(refreshCtx,namespace,previous)
   cache.mu.Lock();defer cache.mu.Unlock()
   entry.refreshing=false;entry.attempted=true;entry.err=err
   if updated!=nil{entry.base=updated}
  }(base)
 }
 attempted,lastErr:=entry.attempted,entry.err
 cache.mu.Unlock()
 if base==nil {
  code:="CoverageWarming";if attempted&&lastErr!=nil{code="CoverageUnavailable"}
  return nil,&serving.Error{Code:code,Message:"측정 커버리지 입력을 준비하지 못했습니다"}
 }
 if time.Since(base.oldest())>5*time.Minute{return nil,&serving.Error{Code:"CoverageUnavailable",Message:"측정 커버리지 입력의 유효 시간이 지났습니다"}}
 if query.Node!="" {
  found:=false;for _,class:=range base.inventory.Classes{for _,node:=range class.Nodes{found=found||node.Name==query.Node}}
  if !found{return nil,&serving.Error{Code:"InvalidCoverageQuery",Message:"현재 inventory의 Node 이름이 필요합니다"}}
 }
 response:=buildMeasurementCoverage(base,namespace,query)
 payload,err:=json.Marshal(response)
 if err==nil&&int64(len(payload))>o.opts.maxRequestBytes{return nil,&serving.Error{Code:"CoverageUnavailable",Message:"커버리지 응답이 크기 한도를 초과했습니다"}}
 return payload,err
}

func (b *measurementCoverageBase) oldest()time.Time{
 var oldest time.Time
 for _,at:=range b.checked{if oldest.IsZero()||at.Before(oldest){oldest=at}}
 return oldest
}

func (o *measurementService) readConsoleCoverage(ctx context.Context,path string,target any)error{
 if o.console==nil{return errors.New("Console is unavailable")}
 u:=*o.console;u.Path=path
 request,err:=http.NewRequestWithContext(ctx,http.MethodGet,u.String(),nil);if err!=nil{return err}
 response,err:=o.http.Do(request);if err!=nil{return err}
 defer response.Body.Close()
 if response.StatusCode!=http.StatusOK{return errors.New("Console coverage source is unavailable")}
 raw,err:=io.ReadAll(io.LimitReader(response.Body,o.opts.maxRequestBytes+1));if err!=nil{return err}
 if int64(len(raw))>o.opts.maxRequestBytes{return errors.New("Console coverage source exceeds response bound")}
 return json.Unmarshal(raw,target)
}

func(o *measurementService)readCoverage(ctx context.Context,namespace string,previous *measurementCoverageBase)(*measurementCoverageBase,error){
 next:=&measurementCoverageBase{checked:map[string]time.Time{},failures:map[string]error{}}
 if previous!=nil{*next=*previous;next.checked=map[string]time.Time{};for kind,at:=range previous.checked{next.checked[kind]=at};next.failures=map[string]error{}}
 // Independent source reads share the deadline; one namespace refresh owns them.
 type sourceResult struct{kind string;err error;at time.Time}
 results:=make(chan sourceResult,5)
 go func(){var value operatorconsole.InventoryView;err:=o.readConsoleCoverage(ctx,"/api/v1/inventory",&value);if err==nil{next.inventory=value};results<-sourceResult{"inventory",err,time.Now().UTC()}}()
 go func(){var value operatorconsole.ModelCompatibilityMatrixView;err:=o.readConsoleCoverage(ctx,"/api/v1/model-compatibility-matrix",&value);if err==nil{next.compatibility=value};results<-sourceResult{"compatibility",err,time.Now().UTC()}}()
 go func(){var value api.BatchEnergyOptimizationList;err:=o.client.List(ctx,&value,client.InNamespace(namespace));if err==nil{next.requests=value};results<-sourceResult{"BEO",err,time.Now().UTC()}}()
 go func(){var value api.ProfilingRunList;err:=o.client.List(ctx,&value,client.InNamespace(namespace));if err==nil{next.runs=value};results<-sourceResult{"Run",err,time.Now().UTC()}}()
 go func(){value,err:=o.scheduler.measurementCatalogView(ctx);at:=value.SourceCheckedAt;if err==nil&&at.IsZero(){err=errors.New("catalog source has not been confirmed")};if err==nil{next.catalog=value;next.adopted=map[string]bool{};if adopted:=o.scheduler.profiles.Load();adopted!=nil{for _,profile:=range adopted.Profiles{next.adopted[profile.ID]=true}};if value.SourceError!=""{err=errors.New(value.SourceError)}};results<-sourceResult{"catalog",err,at}}()
 var failures []error
 for range 5 {result:=<-results;if result.err!=nil{next.failures[result.kind]=result.err;failures=append(failures,result.err);if result.kind=="catalog"&&!result.at.IsZero(){next.checked[result.kind]=result.at}}else{next.checked[result.kind]=result.at}}
 if len(next.checked)!=5{return nil,errors.Join(failures...)}
 // Stable source order, not LIST iteration order, determines the projection.
 next.requests.Items=append([]api.BatchEnergyOptimization{},next.requests.Items...)
 slices.SortFunc(next.requests.Items,func(a,b api.BatchEnergyOptimization)int{if a.Name<b.Name{return -1};if a.Name>b.Name{return 1};return 0})
 return next,errors.Join(failures...)
}
